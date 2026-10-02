package shop

import (
	"errors"
	"net/http"
)

const employeeCookie = "shop_employee"

func (a *App) registerEmployeeRoutes() {
	a.mux.HandleFunc("GET /handheld/employee", a.employeeTransport(a.showEmployee))
	a.mux.HandleFunc("POST /handheld/employee/practice", a.employeeTransport(a.employeeAction))
	a.mux.HandleFunc("POST /handheld/employee/{id}/{action}", a.employeeTransport(a.employeeAction))
}
func (a *App) employeeTransport(next http.HandlerFunc) http.HandlerFunc {
	return a.handheldTransport(func(w http.ResponseWriter, r *http.Request) {
		if !a.config.DemoMode {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	})
}
func employeeProblem(err error) bool {
	return handheldProblem(err) || errors.Is(err, ErrEmployeeAccess) || errors.Is(err, ErrEmployeeClaim) || errors.Is(err, ErrShopperCapacity) || errors.Is(err, ErrIncomplete) || errors.Is(err, ErrAttention) || errors.Is(err, ErrStock)
}
func (a *App) employeePage(w http.ResponseWriter, r *http.Request, message, problem string) {
	raw := handheldCookieValue(r, employeeCookie)
	employee, err := a.store.EmployeeSession(raw)
	if err != nil {
		if employeeProblem(err) {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
		} else {
			a.fail(w, err)
		}
		return
	}
	if employee.Token != "" {
		raw = employee.Token
		a.setHandheldCookie(w, employeeCookie, HandheldSession{Token: employee.Token, Expires: employee.Expires})
	}
	v, err := a.store.EmployeeQueue(raw)
	if err != nil {
		a.fail(w, err)
		return
	}
	v.Message = message
	v.Error = problem
	a.renderHandheldTemplate(w, "employee", v)
}
func (a *App) showEmployee(w http.ResponseWriter, r *http.Request) {
	message := map[string]string{"practice": "Shared practice orders are ready. Loading them again never resets saved work.", "ready": "Order marked ready. Your work is saved and the next order is waiting.", "release": "Order returned to the shared queue. Saved picks and stock are unchanged."}[r.URL.Query().Get("done")]
	a.employeePage(w, r, message, "")
}
func (a *App) employeeAction(w http.ResponseWriter, r *http.Request) {
	if !a.handheldForm(w, r) {
		return
	}
	raw, csrf := handheldCookieValue(r, employeeCookie), r.PostForm.Get("csrf")
	var err error
	action := r.PathValue("action")
	if r.URL.Path == "/handheld/employee/practice" {
		action = "practice"
		err = a.store.SeedEmployeePractice(raw, csrf)
	} else {
		id, e1 := num(r.PathValue("id"))
		version, e2 := num(r.PostForm.Get("version"))
		if e1 != nil || e2 != nil || id < 1 || version < 1 {
			err = ErrInvalid
		} else {
			switch action {
			case "claim":
				var g HandheldSession
				g, err = a.store.EmployeeClaim(raw, csrf, r.PostForm.Get("command_key"), id, version)
				if err == nil {
					a.setHandheldCookie(w, handheldCookie, g)
					redirect(w, r, "/handheld/")
					return
				}
			case "release":
				err = a.store.EmployeeRelease(raw, csrf, r.PostForm.Get("command_key"), id, version)
			case "ready":
				err = a.store.EmployeeReady(raw, csrf, r.PostForm.Get("command_key"), id, version)
			default:
				http.NotFound(w, r)
				return
			}
		}
	}
	if err != nil {
		if !employeeProblem(err) {
			a.fail(w, err)
			return
		}
		a.employeePage(w, r, "", err.Error())
		return
	}
	redirect(w, r, "/handheld/employee?done="+action)
}
