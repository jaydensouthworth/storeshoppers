package shop

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Chat has an independent shell. It must never call view/viewForSession: loading
// a basket can reconcile expired stock holds, which is not a message side effect.
type OrderMessagesView struct {
	*MessageConversation
	Phone                                        bool
	Path, BackURL, BackLabel                     string
	CommandKey, Draft, Message, Error, SendState string
	AcknowledgedRevision                         int64
	State, ResultKey, ResultKind                 string
	OlderURL, LatestURL, FeedURL                 string
	ViewingOlder                                 bool
}

func (a *App) registerMessageRoutes() {
	a.mux.HandleFunc("GET /orders/{id}/messages", a.showCustomerMessages)
	a.mux.HandleFunc("POST /orders/{id}/messages", a.sendCustomerMessage)
	a.mux.HandleFunc("POST /orders/{id}/messages/check", a.checkCustomerMessage)
	a.mux.HandleFunc("GET /handheld/messages", a.handheldTransport(a.showPhoneMessages))
	a.mux.HandleFunc("POST /handheld/messages", a.handheldTransport(a.sendPhoneMessage))
	a.mux.HandleFunc("POST /handheld/messages/check", a.handheldTransport(a.checkPhoneMessage))
}
func messageCustomerCookie(r *http.Request) string {
	c, err := r.Cookie("shop_session")
	if err != nil {
		return ""
	}
	return c.Value
}
func messageQuery(r *http.Request) MessageQuery {
	q := MessageQuery{}
	if r.URL.Query().Has("after") && r.URL.Query().Has("before") {
		q.After = -1
		return q
	}
	for name, dst := range map[string]*int64{"after": &q.After, "before": &q.Before} {
		if raw := r.URL.Query().Get(name); raw != "" {
			n, err := num(raw)
			if err != nil || n < 0 {
				n = -1
			}
			*dst = n
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			n = -1
		}
		q.Limit = n
	}
	return q
}
func (a *App) messageConversation(r *http.Request, phone bool, q MessageQuery) (*MessageConversation, error) {
	if phone {
		return a.store.HandheldMessages(handheldCookieValue(r, handheldCookie), q)
	}
	id, err := num(r.PathValue("id"))
	if err != nil || id < 1 {
		return nil, ErrNotFound
	}
	return a.store.CustomerMessages(id, messageCustomerCookie(r), q)
}
func (a *App) messageAccessError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, ErrEmployeePractice) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Messages-Access", "ended")
		http.Error(w, err.Error(), http.StatusForbidden)
		return true
	}
	if errors.Is(err, ErrHandheldAccess) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrMessageCSRF) || errors.Is(err, ErrHandheldCSRF) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Messages-Access", "ended")
		w.Header().Set("X-Messages-State", "revoked")
		http.Error(w, "This conversation is unavailable or this device's access ended. Return to the order or phone workspace before continuing.", http.StatusForbidden)
		return true
	}
	return false
}
func newMessagesView(v *MessageConversation, phone bool) OrderMessagesView {
	out := OrderMessagesView{MessageConversation: v, Phone: phone, CommandKey: token(), SendState: "idle", BackLabel: "Back to order"}
	out.Path = fmt.Sprintf("/orders/%d/messages", v.OrderID)
	out.BackURL = fmt.Sprintf("/orders/%d", v.OrderID)
	if phone {
		out.Path = "/handheld/messages"
		out.BackURL = "/handheld/"
		out.BackLabel = "Back to pick list"
	}
	out.LatestURL = out.Path
	out.FeedURL = out.Path + "?feed=1"
	if v.HasOlder && v.OldestRevision > 0 {
		out.OlderURL = fmt.Sprintf("%s?before=%d", out.Path, v.OldestRevision)
	}
	return out
}
func messageState(v OrderMessagesView) string {
	if v.ReadOnlyReason == ErrMessageLimit.Error() {
		return "quota"
	}
	if v.Status != "Placed" && v.Status != "Picking" {
		return "closed"
	}
	if v.AssignmentID == 0 {
		return "unassigned"
	}
	return "open"
}
func (a *App) renderMessages(w http.ResponseWriter, r *http.Request, v OrderMessagesView, part string) {
	v.State = messageState(v)
	var body boundedResponseBody
	name := "order-messages"
	if part == "feed" {
		name = "order-messages-feed"
	} else if part == "composer" {
		name = "order-messages-composer"
	}
	if err := a.templates.ExecuteTemplate(&body, name, v); err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// Preserve Origin for the plain-HTML form fallback while suppressing
	// referrers to other origins. Keep the global Origin/CSRF guards strict.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Add("Vary", "HX-Request")
	w.Header().Set("X-Messages-State", messageState(v))
	w.Header().Set("X-Messages-Context", fmt.Sprintf("%s/%d/%d", v.ConversationKey, v.AssignmentID, v.AssignmentVersion))
	w.Header().Set("X-Messages-Context-Version", strconv.FormatInt(v.ContextVersion, 10))
	w.Header().Set("X-Messages-Revision", strconv.FormatInt(v.Revision, 10))
	w.Header().Set("X-Messages-Expires", strconv.FormatInt(v.Expires, 10))
	if v.ResultKind != "" {
		w.Header().Set("X-Messages-Result", v.ResultKind)
	}
	if v.ResultKey != "" {
		w.Header().Set("X-Messages-Key", v.ResultKey)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}
func (a *App) showCustomerMessages(w http.ResponseWriter, r *http.Request) {
	a.showMessages(w, r, false)
}
func (a *App) showPhoneMessages(w http.ResponseWriter, r *http.Request) { a.showMessages(w, r, true) }
func (a *App) showMessages(w http.ResponseWriter, r *http.Request, phone bool) {
	q := messageQuery(r)
	c, err := a.messageConversation(r, phone, q)
	if err != nil {
		if a.messageAccessError(w, err) {
			return
		}
		if errors.Is(err, ErrInvalid) {
			http.Error(w, "Invalid message page cursor.", http.StatusBadRequest)
			return
		}
		a.fail(w, err)
		return
	}
	v := newMessagesView(c, phone)
	v.ViewingOlder = q.Before > 0
	part := ""
	if r.URL.Query().Get("feed") == "1" {
		part = "feed"
	}
	a.renderMessages(w, r, v, part)
}
func (a *App) parseMessageForm(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != a.config.Origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Cross-origin message request rejected.", http.StatusForbidden)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		w.Header().Set("X-Messages-Result", "invalid")
		http.Error(w, "The message form is invalid or too large. Nothing was sent.", http.StatusBadRequest)
		return false
	}
	return true
}
func messageForm(r *http.Request) MessageCommand {
	c := MessageCommand{ConversationKey: r.PostForm.Get("conversation_key"), Key: r.PostForm.Get("command_key"), Body: r.PostForm.Get("body")}
	c.AssignmentID, _ = num(r.PostForm.Get("assignment_id"))
	c.AssignmentVersion, _ = num(r.PostForm.Get("assignment_version"))
	return c
}
func messageDraft(body string) string {
	// A failed oversized request remains bounded display-only text. Do not silently
	// normalize it into a valid send: the user must review and submit deliberately.
	body = strings.ToValidUTF8(body, "�")
	runes := []rune(body)
	if len(runes) > MessageMaxCodepoints+1 {
		body = string(runes[:MessageMaxCodepoints+1])
	}
	for len(body) > MessageMaxBytes+4 {
		_, n := utf8.DecodeLastRuneInString(body)
		body = body[:len(body)-n]
	}
	return body
}
func messageProblem(err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) || errors.Is(err, ErrMessageClosed) || errors.Is(err, ErrMessageRate) || errors.Is(err, ErrMessageLimit)
}
func (a *App) sendCustomerMessage(w http.ResponseWriter, r *http.Request) { a.sendMessage(w, r, false) }
func (a *App) sendPhoneMessage(w http.ResponseWriter, r *http.Request)    { a.sendMessage(w, r, true) }
func (a *App) sendMessage(w http.ResponseWriter, r *http.Request, phone bool) {
	if !a.parseMessageForm(w, r) {
		return
	}
	c := messageForm(r)
	var result MessageResult
	var err error
	if phone {
		result, err = a.store.SendHandheldMessage(handheldCookieValue(r, handheldCookie), r.PostForm.Get("csrf"), c)
	} else {
		id, e := num(r.PathValue("id"))
		if e != nil || id < 1 {
			err = ErrNotFound
		} else {
			result, err = a.store.SendCustomerMessage(id, messageCustomerCookie(r), r.PostForm.Get("csrf"), c)
		}
	}
	if !phone && errors.Is(err, ErrMessageCSRF) {
		a.recoverCustomerMessageForm(w, r, c)
		return
	}
	if a.messageAccessError(w, err) {
		return
	}
	if err != nil && !messageProblem(err) {
		a.fail(w, err)
		return
	}
	// Re-authorize before projecting; a post-send revocation must not disclose a
	// now-forbidden transcript just to acknowledge an earlier successful write.
	conversation, viewErr := a.messageConversation(r, phone, MessageQuery{})
	if viewErr != nil {
		if !a.messageAccessError(w, viewErr) {
			a.fail(w, viewErr)
		}
		return
	}
	v := newMessagesView(conversation, phone)
	if err == nil {
		if r.Header.Get("HX-Request") != "true" {
			http.Redirect(w, r, v.Path, http.StatusSeeOther)
			return
		}
		v.SendState = "saved"
		v.ResultKind = "sent"
		v.ResultKey = c.Key
		v.AcknowledgedRevision = result.Message.Revision
		v.Message = "Message sent and saved in this demo order."
		if result.Replayed {
			v.Message = "This message was already saved. No duplicate was sent."
		}
	} else {
		v.SendState = "rejected"
		v.ResultKind = "invalid"
		v.Draft = messageDraft(c.Body)
		v.Error = err.Error()
		if errors.Is(err, ErrConflict) {
			v.ResultKind = "stale"
			v.Error = "The assignment or message changed. Your draft is kept. Review the current recipient, then send deliberately."
		}
		if errors.Is(err, ErrMessageLimit) {
			v.CanSend = false
			v.ReadOnlyReason = err.Error()
		}
	}
	part := ""
	if r.Header.Get("HX-Request") == "true" {
		part = "composer"
	}
	a.renderMessages(w, r, v, part)
}
func (a *App) checkCustomerMessage(w http.ResponseWriter, r *http.Request) {
	a.checkMessage(w, r, false)
}
func (a *App) checkPhoneMessage(w http.ResponseWriter, r *http.Request) { a.checkMessage(w, r, true) }
func (a *App) checkMessage(w http.ResponseWriter, r *http.Request, phone bool) {
	if !a.parseMessageForm(w, r) {
		return
	}
	c := messageForm(r)
	conversation, err := a.messageConversation(r, phone, MessageQuery{})
	if err != nil {
		if !a.messageAccessError(w, err) {
			a.fail(w, err)
		}
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(conversation.CSRF)) != 1 {
		if !phone {
			a.recoverCustomerMessageForm(w, r, c)
		} else {
			a.messageAccessError(w, ErrHandheldCSRF)
		}
		return
	}
	var found *OrderMessage
	if phone {
		found, err = a.store.FindHandheldMessage(handheldCookieValue(r, handheldCookie), c.ConversationKey, c.Key)
	} else {
		found, err = a.store.FindCustomerMessage(conversation.OrderID, messageCustomerCookie(r), c.ConversationKey, c.Key)
	}
	if a.messageAccessError(w, err) {
		return
	}
	if err != nil && !messageProblem(err) {
		a.fail(w, err)
		return
	}
	// The lookup also authorizes. Reproject again so a lifecycle change between
	// these read snapshots cannot re-enable a stale composer or reveal old data.
	current, viewErr := a.messageConversation(r, phone, MessageQuery{})
	if viewErr != nil {
		if !a.messageAccessError(w, viewErr) {
			a.fail(w, viewErr)
		}
		return
	}
	v := newMessagesView(current, phone)
	v.Draft = messageDraft(c.Body)
	normalized, bodyErr := normalizeMessageBody(c.Body)
	switch {
	case err != nil:
		v.SendState = "rejected"
		v.ResultKind = "stale"
		v.Error = "The conversation or request changed. Review the current recipient and your draft before sending."
	case found != nil && bodyErr == nil && found.Body == normalized && found.AssignmentID == c.AssignmentID && found.AssignmentVersion == c.AssignmentVersion:
		v.SendState = "saved"
		v.ResultKind = "sent"
		v.ResultKey = c.Key
		v.AcknowledgedRevision = found.Revision
		v.Draft = ""
		v.Message = "Your message is saved. No duplicate was sent."
	case found != nil:
		v.SendState = "rejected"
		v.ResultKind = "stale"
		v.Error = "That earlier request saved different text or assignment context. Your current draft is kept as a new message for review."
	case c.ConversationKey != current.ConversationKey || c.AssignmentID != current.AssignmentID || c.AssignmentVersion != current.AssignmentVersion || !current.CanSend:
		v.SendState = "rejected"
		v.ResultKind = "stale"
		v.Error = "The assignment or conversation state changed. The old request cannot be newly sent in this state. Review your retained draft and current recipient."
	default:
		v.SendState = "unconfirmed"
		v.ResultKind = "absent"
		v.CommandKey = c.Key
		v.Message = "No saved result is visible yet. This does not prove an earlier request cannot still finish. Check again or retry the same unchanged message."
	}
	part := ""
	if r.Header.Get("HX-Request") == "true" {
		part = "composer"
	}
	a.renderMessages(w, r, v, part)
}

// Manager sign-in/out rotates CSRF inside the same customer session. This is
// not a new conversation or lost ownership. Recover only that exact owner and
// epoch-bound conversation, using read-only result reconciliation; never send.
// Phone CSRF cannot use this path because rotation there may mean a new grant.
func (a *App) recoverCustomerMessageForm(w http.ResponseWriter, r *http.Request, c MessageCommand) {
	conversation, err := a.messageConversation(r, false, MessageQuery{})
	if err != nil {
		if !a.messageAccessError(w, err) {
			a.fail(w, err)
		}
		return
	}
	if c.ConversationKey != conversation.ConversationKey {
		a.messageAccessError(w, ErrNotFound)
		return
	}
	found, err := a.store.FindCustomerMessage(conversation.OrderID, messageCustomerCookie(r), c.ConversationKey, c.Key)
	if a.messageAccessError(w, err) {
		return
	}
	if err != nil && !messageProblem(err) {
		a.fail(w, err)
		return
	}
	current, viewErr := a.messageConversation(r, false, MessageQuery{})
	if viewErr != nil {
		if !a.messageAccessError(w, viewErr) {
			a.fail(w, viewErr)
		}
		return
	}
	if c.ConversationKey != current.ConversationKey {
		a.messageAccessError(w, ErrNotFound)
		return
	}
	v := newMessagesView(current, false)
	v.Draft = messageDraft(c.Body)
	body, bodyErr := normalizeMessageBody(c.Body)
	switch {
	case found != nil && bodyErr == nil && found.Body == body && found.AssignmentID == c.AssignmentID && found.AssignmentVersion == c.AssignmentVersion:
		v.SendState = "saved"
		v.ResultKind = "sent"
		v.ResultKey = c.Key
		v.AcknowledgedRevision = found.Revision
		v.Draft = ""
		v.Message = "Your earlier message is already saved. The form was refreshed; no duplicate was sent."
	case found == nil && err == nil && bodyErr == nil && c.AssignmentID == current.AssignmentID && c.AssignmentVersion == current.AssignmentVersion:
		// An earlier read-only check may already have paired this same key with
		// current CSRF. Absence must never fork another sendable key for the same
		// intent. Refresh only credentials; keep body, key and assignment exact.
		v.SendState = "unconfirmed"
		v.ResultKind = "absent"
		v.CommandKey = c.Key
		v.Message = "Your form was refreshed and the exact draft is kept. No saved result is visible yet. Check again or explicitly retry the same message; nothing was sent automatically."
	default:
		// A changed/invalid payload or assignment needs a new deliberate review.
		// This response creates only a reviewed form, never another message.
		v.SendState = "rejected"
		v.ResultKind = "stale"
		v.Error = "Your message form was refreshed after the workspace changed. Nothing new was sent. Review your retained draft and current recipient before sending."
		if found != nil {
			v.Error = "The earlier request saved different text or assignment context. Your draft is kept for review in this refreshed form."
		}
	}
	part := ""
	if r.Header.Get("HX-Request") == "true" {
		part = "composer"
	}
	a.renderMessages(w, r, v, part)
}
