# Manager catalog refinement

The Products catalog defaults to active products sorted by name. Managers can combine:

- Bounded text search across product names, permanent SKUs, legacy barcode fields, stored raw/normalized local identifiers, departments and product types
- Stable department and optional product-type identities, including an explicit “No product type” choice
- Available stock, low stock (1–7 counted units), or zero stock
- Active, archived, or both catalog states
- Name, SKU, newest identity, or unit-price sorting

Available stock excludes reserved basket units. Grams never enter the counted low-stock filter. Unit prices retain their per-item/per-kilogram labels, and weighted ordering follows the gram bounds and review rules in [Weighted products](WEIGHTED_PRODUCTS.md). Archived identities remain searchable by choosing archived or all products; public scanner and checkout eligibility are unchanged.

The search field stays visible. Secondary controls use a native, initially closed disclosure and normal GET form, with optional HTMX enhancement. The result count and clear action remain visible outside the disclosure. Add product appears only in the catalog header; the main manager navigation points to Products, rather than placing a create action in every section.

Products are paginated in groups of 20 after filtering and deterministic sorting, with ID tie-breaks, an accurate total and visible result range. Previous/next links preserve every filter. A changed search or filter starts at page 1. Page parameters are bounded to 1–1,000,000 and normalized to the available page range; empty results use page 1. Normal HTML redirects to the canonical page, while HTMX replaces its history URL. Taxonomy options and product editor lookup always use the complete catalog.

List, create and dedicated edit screens carry the same bounded, allowlisted filter and page query context through normal links, hidden form fields, redirects and HTMX history updates. The current editor is loaded before list filtering, so changing a product or a filter cannot hide that product’s editor. No arbitrary return URL is accepted. Unknown numeric taxonomy IDs are visibly labelled and return no matching products rather than silently appearing to be an “all” filter.

Catalog filtering is read-only. Existing CSRF, manager checks, version guards, stock reservations and checkout quote rules remain authoritative. Tests cover individual/combined refinements, both stored code representations, archive states, sorting and page-boundary tie breaks, pagination/empty/out-of-range cases, context across create/edit/archive/restore/validation/conflicts, HTML/HTMX responses, guards and compact mobile markup.
