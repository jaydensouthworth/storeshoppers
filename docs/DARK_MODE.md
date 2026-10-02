# Appearance preferences

The storefront and manager share a compact sun/moon button in the header. It
switches the current light/dark appearance without opening a menu. Its stable
accessible name is **Dark mode**, with a pressed state and a tooltip describing
the next action. The transparent 44-pixel button keeps a usable touch target
without competing with navigation.

The default follows the operating system. A saved explicit choice takes priority;
**Use system appearance** in the footer restores the default. The footer names
the current preference, and System mode responds to operating-system changes
while the page is open.

The choice is saved in this origin's browser local storage as
`neighborhood-market-theme`. It survives full navigation, reloads, manager
sign-in/sign-out and a demo session reset. It contains only an appearance
preference, not identity or shopping data. Other tabs receive preference updates.
If browser storage is unavailable or full, the choice still applies to the current
page and HTMX updates; a brief message explains that it could not be saved.

## Implementation

- `web/static/theme.js` is a small, local, head-loaded script that applies the
  preference before the document paints. It uses delegated events and restores
  the button state after HTMX workspace swaps and browser history navigation
- `web/static/theme.css` separates page, surface, form, feedback and branding
  colors. Navy signs keep white text; yellow labels keep navy text; red price
  stickers keep white text. Illustrations retain their original vector colors
- Both the ordinary layout and standalone reset pages use the same assets and
  header button and footer action. No server setting, database migration, account or external dependency
  is needed. The existing Content Security Policy permits the local assets
- Without JavaScript, CSS follows the system theme and the inactive appearance controls are
  hidden. Regular forms and navigation continue to work
- Native buttons provide Enter/Space keyboard activation. Delegated clicks also
  work on the nested SVG icons and controls replaced by HTMX. Reduced-motion
  settings remove product hover movement and existing transitions
- Compact weekly offer cards and the basket bar use theme surfaces; the weekly
  stamp remains plain copy. Theme rules do not alter circular or featured-shelf
  grid dimensions, text sizing or spacing
- Inline picking, add/remove editors, product-picker selections and unsaved-form
  recovery use the same color roles. Selected product rows keep navy text and
  readable metadata on yellow; picked counts and add controls keep white on navy

## Checks

Run the application checks as usual:

```sh
./scripts/check.sh
```

With Node.js available, run the dependency-free preference and color checks:

```sh
node --check web/static/theme.js
node --test scripts/theme_test.cjs
```

These checks cover initial system selection, explicit overrides, storage
failures, reload persistence, HTMX replacement, history restoration, cross-tab
updates, older media-query listeners, template asset loading, text contrast for the
defined palettes, input boundaries and focus indicators.
The script harness is not a browser or a substitute for visual verification.

Before a release, inspect the running version in a supported browser at desktop
width and 400 CSS pixels (the scripted checks do not verify layout):

1. Toggle light/dark and return to System using the keyboard; check names, pressed
   state, focus, header fit and persistence after refresh and full-page navigation
2. In System mode, change the operating-system appearance; then verify that an
   explicit Light or Dark choice ignores the system setting
3. Filter the storefront and add products using HTMX, then visit product details,
   basket, receipt, order tracking and manager sign-in
4. Check the compact weekly band, basket bar and mixed regular/sale featured
   rows in both palettes. The theme should not change their geometry or push the
   catalog farther down the page
5. Inspect manager navigation, products and illustration selection, categories,
   stock/activity, promotions, shoppers and basket holds. Open quantity,
   substitution and removal editors; select a product and check picked counts,
   unsaved quantities and sign-in recovery
6. Check error, successful, expired, disabled, archived and empty states, native
   form controls and red/yellow price or status labels. Inspect reset confirmation
   and success pages using disposable fake data
7. Test reduced motion, storage unavailable and JavaScript disabled. Verify that
   the core HTML shopping and manager workflows remain usable

## Demo management guide

Demo storefront and product discovery pages include a small nonmodal callout
pointing to **Employees**, with “Click here to try store management” and an
accessible close button. It is omitted from baskets, orders, manager and normal
(non-demo) pages. Its position follows the visible link, stays inside the
viewport, and avoids the fixed mobile basket bar. No animation or initial focus
change is required.

Dismissal is remembered locally, with session storage as a fallback. If neither
store can remember a choice, the guide starts hidden. **Show demo guide** in the
footer reopens it deliberately. Clicking Employees also completes the guide.
Keyboard dismissal, clear screen-reader instructions and theme contrast accompany
the visual arrow; normal navigation and checkout remain available.
