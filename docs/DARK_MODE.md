# Appearance preferences

The storefront and manager share the **Theme** selector in the header. Choose
**System**, **Light** or **Dark**. The default follows the operating system, and a
saved explicit choice takes priority until the visitor changes it. System mode
responds to operating-system changes while the page is open.

The choice is saved in this origin's browser local storage as
`neighborhood-market-theme`. It survives full navigation, reloads, manager
sign-in/sign-out and a demo session reset. It contains only an appearance
preference, not identity or shopping data. Other tabs receive preference updates.
If browser storage is unavailable or full, the choice still applies to the current
page and HTMX updates; a brief message explains that it could not be saved.

## Implementation

- `web/static/theme.js` is a small, local, head-loaded script that applies the
  preference before the document paints. It uses delegated events and restores
  the selector after HTMX workspace swaps and browser history navigation
- `web/static/theme.css` separates page, surface, form, feedback and branding
  colors. Navy signs keep white text; yellow labels keep navy text; red price
  stickers keep white text. Illustrations retain their original vector colors
- Both the ordinary layout and standalone reset pages use the same assets and
  selector. No server setting, database migration, account or external dependency
  is needed. The existing Content Security Policy permits the local assets
- Without JavaScript, CSS follows the system theme and the inactive selector is
  hidden. Regular forms and navigation continue to work
- A native labelled select provides keyboard control. Its label sits beside the
  44-pixel control so it fits the normal header height; navigation uses the existing
  850-pixel wrapping breakpoint. At very narrow widths the label stays available
  to assistive technology without consuming space. Reduced-motion settings remove
  product hover movement as well as existing transitions
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

1. Switch among all three modes with the keyboard; check labels, focus, header
   fit and persistence after refresh and full-page navigation
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
