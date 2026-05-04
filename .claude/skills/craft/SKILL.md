---
name: craft
description: Build serious software with the discipline of top Go practitioners (Pike, Cheney, Bourgon, Ryer) and the visual craft of top pro-tool designers (Wathan/Schoger, Rauno Freiberg, Linear). Use when writing Go services, when working in a no-build vanilla web UI (single app.js / single style.css, no React, no bundler), and when designing dense, keyboard-driven, information-rich interfaces — pentest tools, IDEs, dashboards, observability UIs. Codifies practices that survive contact with real teams, real engagements, and real users.
---

# Craft

This skill is the operating manual for building tools that look and feel made by people who care. It pulls from three disciplines that converge in any serious developer/operator product:

1. **Go that survives an engineering team** — code another engineer can read in a week, not a month.
2. **Vanilla web UI that ships without a build chain** — one HTML, one CSS, one JS, querySelector and addEventListener, the platform as the framework.
3. **Dense pro-tool design** — Burp / Linear / Proxyman / Postman aesthetics: keyboard-first, information-rich, calm under load.

It is opinionated. Every rule traces back to someone who has shipped at scale (sources at the bottom). When a rule fights the user's explicit instruction, the user wins — but the rule should still be voiced once so the trade-off is conscious.

---

## Part I — Go

### The four laws

1. **Simplicity is the goal, not the side effect.** "A complicated system is the sum of many simple decisions, made with the long-term in mind." (Cheney) If you find yourself writing clever code, the design above the code is wrong. Back up.
2. **Readable code beats elegant code.** Code is read 100x more than it is written. Pick names a new hire could parse in 30 seconds. Single-letter variables only inside trivial loops.
3. **Boring is the highest praise.** "Everything about this codebase is obvious." (Mat Ryer) No magic, no metaprogramming, no cleverness for its own sake.
4. **Industrial Go is different from open-source Go.** (Bourgon) You are writing for a team where engineers come and go. Code outlives any single author. Optimize for the next person, not yourself.

### Project layout

```
cmd/<binary>/main.go        — minimal entrypoint, calls into the package
internal/...                — code that is yours and not for export
pkg/... (only if reused)    — code intended for import by other repos
apps/<service>/             — separate Echo/HTTP services if multiple
```

- `main()` does one thing: parse flags/env, build a `*Server`, call `server.Run(ctx)`. Anything more is too much.
- Avoid `init()`. It runs in unpredictable order, hides side effects, and can't return errors. If you think you need it, you don't.
- `cmd/` files are short. Real logic lives in importable packages so it can be tested.

### HTTP services (the Mat Ryer pattern, refined)

```go
type Server struct {
    db     *sql.DB
    log    *slog.Logger
    cfg    Config
    router http.Handler
}

func NewServer(cfg Config, db *sql.DB, log *slog.Logger) *Server {
    s := &Server{db: db, log: log, cfg: cfg}
    s.routes()                // separate file routes.go — a table of contents
    return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    s.router.ServeHTTP(w, r)
}
```

- `routes.go` is a single function listing every route. New engineers read it once and understand the API surface.
- Handlers are functions that *return* `http.HandlerFunc` so they can close over per-handler dependencies and run setup once at boot:
  ```go
  func (s *Server) handleGetItem() http.HandlerFunc {
      // expensive prep here runs once
      tmpl := template.Must(template.ParseFiles("item.html"))
      return func(w http.ResponseWriter, r *http.Request) {
          // hot path
      }
  }
  ```
- `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` for shutdown. Pass `ctx` everywhere; never store it on a struct.
- Always use `http.Server` directly with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout`. The defaults are footguns.

### Errors

- **Errors are values.** Return them, wrap them with `%w`, handle them once.
  ```go
  if err != nil {
      return fmt.Errorf("fetching user %d: %w", id, err)
  }
  ```
- The wrapping verb is the breadcrumb. Read errors top-down: outer context first, root cause last.
- **Handle each error exactly once.** Either log it OR return it. Never both — that produces double-logged stack traces.
- `errors.Is` and `errors.As` for typed checks; never compare with `==` across package boundaries.
- A `panic` is a contract violation, not a control flow tool. Recover only at goroutine boundaries (HTTP middleware, worker pool entry points). Never `recover()` inside business logic.

### Concurrency

- "Don't communicate by sharing memory; share memory by communicating." (Pike) Channels for orchestration, mutexes for state.
- Every goroutine you start: who stops it? Write the answer in a comment if it isn't obvious. Leaked goroutines are the #1 production bug.
- `context.Context` is the cancellation contract. First parameter, always. Never store on a struct, never pass `nil` — pass `context.TODO()` instead so it's grep-able.
- `sync.WaitGroup` for fan-out, `errgroup.Group` when any failure should cancel the rest. Avoid hand-rolled synchronization with channels of `struct{}{}`.
- Buffered channels are an optimization, not a convenience. Default to unbuffered.

### Interfaces

- **Accept interfaces, return concrete types.** Caller decides what shape they need; you ship something they can introspect.
- Keep interfaces small. The `io.Reader` (one method) is the gold standard. The `io.ReadWriteCloser` (three methods) is the upper limit before you should ask whether two interfaces are hiding inside one.
- Define interfaces in the *consuming* package, not the producing one. The producer doesn't know what shapes consumers want.
- No `IUser`, no `UserService` for a one-method "get a user". Just a function.

### Configuration

- Flags first, then env, then file. (Bourgon) Flags are self-documenting (`--help` lists them) and explicit at boot. Env is for ops to override without rebuilding. Files are for things too big for either.
- All defaults visible in one place. No "secret" config that only kicks in when an env var is set somewhere deep.
- Config is a struct, parsed once in `main`, passed to `NewServer`. No global `viper.GetString("foo")` reads scattered through the code.

### Observability

- **Structured logs** (`slog`) with consistent field names. `request_id`, `user_id`, `duration_ms` — never invent new spellings.
- Log lines are *events*, not stories. One event per line. No multi-line tracebacks except for fatal errors.
- Three log levels are enough: `debug` (verbose, off in prod), `info` (state changes worth seeing), `error` (something needs attention). `warn` is a coward's `error`.
- Metrics: counters and histograms. Never gauges for things that aren't gauges. Histogram bucket boundaries should match your SLO targets, not arbitrary powers of ten.
- Tracing if you're across services; otherwise it's overhead.

### Testing

- **Table-driven tests** are the default. Each row is a self-contained scenario with a `name` field for `t.Run`.
- `t.Parallel()` whenever the test doesn't share global state. CI runtime drops by 5-10x.
- Test against a real database, not a mock. Mocks lie; mocks pass when prod fails. Use `testcontainers` or a per-test SQLite instance.
- Golden files for any output ≥ ~5 lines. Diff is faster to read than `assert.Equal` on a multi-line string.
- `httptest.NewServer` for HTTP integration; never roll your own listener-and-port dance.
- One assertion per failure mode. A test that asserts "the result is correct AND the cache was updated AND a metric fired" is three tests.

### What not to do

- No `interface{}` (or `any`) without a comment explaining why a typed alternative is impossible.
- No reflection except in serializers (`encoding/json`) and reflection-based ORMs (avoid those too).
- No `init()`. (Mentioned twice on purpose.)
- No global mutable state. Package-level `var` is fine if it's effectively `const` after process start.
- No DSLs in YAML or JSON for behavior that should be code. Configuration describes *what*; code describes *how*.

---

## Part II — Vanilla web UI (no build, no framework)

### The premise

A single-page app for a serious tool — proxy, debugger, dashboard — does not need React. The platform shipped most of what frameworks were invented to provide. In 2026 you have:

- ES modules with native imports (`<script type="module">`)
- `<dialog>` with built-in focus trap and backdrop
- `popover="auto"` and `popovertarget` for menus and tooltips
- View Transitions API for animated state changes
- Container queries (`@container`) for component-level responsiveness
- `:has()` selector for parent-state styling
- Scroll-driven animations
- CSS nesting

The framework cost — runtime weight, build complexity, version churn, hiring on niche frameworks — is real. The framework benefit shrinks every year. For internal tools with a known small DOM, vanilla wins on every axis: cold start, hot reload (none needed — just refresh), debuggability, longevity.

### File layout

```
dist/index.html       — single document, semantic markup
dist/assets/app.js    — single script, IIFE-wrapped, no global pollution
dist/assets/style.css — single stylesheet, layered with @layer
```

If you need more than three files, you probably need a build step; if you need a build step, the platform's progress is making your decision worse every month. Re-evaluate.

### JavaScript style

- Wrap everything in an IIFE so nothing leaks to `window`:
  ```js
  (function() {
      'use strict';
      // ...
  })();
  ```
- Use `var` deliberately for anything you'd otherwise declare with `let` at the IIFE's top scope (it's an old codebase signal that says "this is the script's namespace"). `const` for true constants. `let` inside blocks. ES5+ syntax to keep the source diff-able without source maps.
- One pattern, repeated: `$('#foo')` for `document.querySelector`, `$$('.bar', root)` for `root.querySelectorAll`. Define them once at the top.
- **Event delegation** for any list-like UI. Bind one listener at the parent; check `e.target.closest('.row')`. New rows added later don't need to be re-bound.
- **Read DOM, then mutate.** The browser batches reads and writes; mixing them causes layout thrashing. If you must measure-then-write, wrap the write in `requestAnimationFrame`.
- `innerHTML =` for big rebuilds (it's the fastest path for >5 nodes); manual `appendChild` for surgical updates. Both are fine. Don't pretend the DOM is your enemy.
- Escape user content. A 4-line `escapeHtml` helper is plenty:
  ```js
  function escapeHtml(s) {
      return String(s).replace(/[&<>"']/g, function(c) {
          return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c];
      });
  }
  ```
- State lives in the DOM and `localStorage`, not in a JS object you have to keep in sync. The DOM is your store. If you need cross-component coordination, use `CustomEvent` on `document`.
- Async: `async`/`await` over `.then()` chains. `try`/`catch` around every `await` that touches the network — never let a rejected promise leak to the console for the user to find.

### CSS style

- One file. Use `@layer` to enforce cascade order without specificity wars:
  ```css
  @layer reset, base, layout, components, utilities, theme;
  ```
- Custom properties (`--var`) at `:root` for the design system; per-theme overrides on `[data-theme="..."]`. Toggle the attribute, the whole UI re-themes.
- **Spacing is a system, not a vibe.** Pick a base (`--space-1: 4px`) and only use multiples (`var(--space-2)`, `var(--space-3)`, ...). No magic `padding: 13px`.
- **Type scale is geometric.** A modular scale (`1.125`, `1.2`, `1.25`) gives you three or four sizes that look intentional. Pick one and stick with it: `--text-xs: 11px`, `--text-sm: 12px`, `--text-base: 13px`, `--text-lg: 15px`, `--text-xl: 18px`. Pro tools live at 12-13px body.
- **Semantic colors, not raw hex.** `--accent`, `--accent-dim`, `--text-primary`, `--text-secondary`, `--bg`, `--bg-elev`, `--border`, `--danger`, `--warn`, `--success`. Theme overrides change variables, never selectors.
- `box-sizing: border-box` everywhere. Every project, first rule.
- `min-width: 0` on flex children that can overflow — the default `min-width: auto` is the source of half the "why is this not shrinking" bugs.
- Avoid `!important`. If you're reaching for it, the cascade is wrong upstream.

### Overlay editors (the lorg-style transparent textarea on highlighted `<pre>`)

This pattern shows up everywhere and is the #1 source of "cursor is misaligned" bugs. The rule is:

> The textarea and the `<pre>` must produce **byte-identical text**, **identical whitespace**, **identical font metrics**, and **synchronized scroll**. Any deviation drifts the caret.

Checklist when building or debugging one:

- Same `font-family`, `font-size`, `font-weight`, `line-height`, `letter-spacing`, `word-spacing`, `tab-size`, `white-space`, `word-break`, `padding`, `border`, `box-sizing`. Every one. Diff `getComputedStyle` between the two elements while developing.
- The highlighted `<pre>` must contain the **exact same characters** as the textarea, including trailing newlines. Empty content needs a fallback `'\n'` so the `<pre>` retains its line height.
- `pointer-events: none` on the `<pre>` so clicks pass through to the textarea.
- Scroll sync: `textarea.addEventListener('scroll', () => { pre.scrollTop = textarea.scrollTop; pre.scrollLeft = textarea.scrollLeft; })`.
- **Avoid mixing font weights inside the highlight spans.** Bold (700) and regular (400) glyphs in monospace fonts have different sub-pixel widths, even when nominally fixed-width. A 0.5-1 px drift accumulates per weight boundary, and over 30 columns the caret is visibly off the highlighted character. Differentiate roles by **color**, not weight, inside the overlay. Save bold for non-overlay contexts (read-only response panes).
- Make the textarea text transparent (`color: transparent; -webkit-text-fill-color: transparent`) and color the caret with `caret-color`. The colored characters come from the `<pre>` underneath.

### Performance (only when it matters)

- Don't optimize until you've measured. The browser is faster than you think.
- Long lists: virtualize at ~500 rows. Below that, just render them all.
- `requestIdleCallback` for non-urgent work; `requestAnimationFrame` for anything visual.
- Debounce input handlers (search, filter) with a 100-150ms timer. Throttle scroll handlers. Don't both.
- `IntersectionObserver` instead of `scroll` + `getBoundingClientRect()` for "is this visible" checks.

### Accessibility (it's not optional)

- Every interactive element is reachable by keyboard. `<button>` not `<div onclick>`. `<a href>` not `<span onclick>`. The platform gives you focus, role, and Enter/Space activation for free.
- Visible focus rings. Browsers strip them by default; put them back: `:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }`.
- `aria-live="polite"` regions for transient status (e.g., "Saved", "Sent"). Screen readers announce them without stealing focus.
- `prefers-reduced-motion` honored on every animation: `@media (prefers-reduced-motion: reduce) { * { animation-duration: 0.001ms !important; } }`.
- Color contrast ≥ 4.5:1 for body text, ≥ 3:1 for large text and UI elements. Test against your dark backgrounds, not just light ones.
- `<dialog>` for modals (free focus trap, Escape close, backdrop). Manual modal implementations are wrong almost every time.

---

## Part III — UI/UX for dense, serious tools

### The aesthetic

Think Burp Suite, Linear, Proxyman, Figma's developer panels, the Vercel dashboard. The shared DNA:

- Dark by default, with a true light theme that isn't an afterthought.
- 12-13 px monospace body text. Generous line-height (1.4-1.6). Whitespace is structural, not decorative.
- **Calm color**: 90% grays, 10% color. A single accent that means "active / your attention is here". Status colors used only for status (`success`, `warn`, `danger`).
- No drop shadows on flat surfaces. Use 1 px borders or subtle background-elevation tints (`--bg-elev`) for layering.
- Density without clutter. Tables crammed with information are fine if alignment, grouping, and color contrast carry the eye through them.

### Refactoring UI in 7 rules

(Adam Wathan & Steve Schoger, distilled)

1. **Start with a feature, not a layout.** Build the smallest unit that delivers value, then compose.
2. **Hierarchy is built with size, weight, and color** — in that order. Borders are a last resort.
3. **Don't design with grays first.** Greyscale mockups look fine and ship as bland UIs. Add the accent and the warning red and the success green from the start.
4. **Spacing systems beat freehand spacing.** Pick a scale (4 px or 8 px base) and snap everything to it. The eye notices misalignment by 1 px.
5. **Use depth sparingly.** A whole UI of cards-on-cards-on-cards is exhausting. Reserve elevation for one or two truly important moments (active modal, focused panel).
6. **Imagery and emoji should earn their place.** Most pro tools have neither. If you add one, it's a deliberate choice carrying meaning.
7. **Polish the empty, loading, and error states.** A great UI is a UI that's great when there's no data, when the network is slow, and when something broke.

### Information density without clutter

- **Alignment is structure.** Left-align labels and values to the same column. Right-align numbers so digits stack. Never center body content.
- **Group, don't separate.** A list of 20 items with consistent spacing reads as a unit. Adding dividers between every row breaks the unit into 20 fragments.
- **Truncate, don't wrap.** For data tables, ellipsize and reveal full text on hover or click. Wrapping rows destroys vertical scan-ability.
- **Sticky headers** when the table scrolls. Always. Pro tools have this; toy tools don't.
- **Sortable, filterable, searchable** — every table. Cmd/Ctrl+F focuses the filter; clicking a header sorts; one keystroke does everything.

### Keyboard-first design (the Linear lesson)

A pro tool is a tool you use for hours a day. The keyboard is the bandwidth ceiling.

- **Cmd/Ctrl+K command palette.** Fuzzy search over every action. New users learn the app by typing what they want; experts memorize 5-10 commands and forget the menu exists.
- **Single-letter shortcuts** when the textarea isn't focused (`g h` to go home, `j`/`k` to navigate, `e` to edit, `?` for the cheatsheet).
- **Escape always closes** the topmost overlay (modal, dropdown, palette, find-bar). One key, predictable.
- **Tab order matches visual order.** Test it. Most tab orders in production UIs are wrong because nobody tests it.
- **Focus rings visible** even when `body` has `outline: 0` set. `:focus-visible` is your friend.
- **Mouse and keyboard parity.** Every keyboard shortcut should have a discoverable mouse equivalent and vice versa. The tooltip on a button shows its shortcut.

### Microinteractions (the Rauno Freiberg lesson)

Small details, repeated thousands of times, are what make a UI feel like a finished product instead of a wireframe.

- **Feedback within 100 ms.** A click that takes 300 ms to visibly respond feels broken. If the work is slow, show a *spinner or a skeleton within 100 ms* and the result whenever it's ready.
- **Animations 150-300 ms with `ease-out`.** Faster feels jarring; slower feels sluggish. Linear/exponential easing is wrong for UI; ease-out matches user expectation that motion decelerates.
- **Anticipation and follow-through** (Disney's animation principles, applied to UI). A modal opens by scaling slightly past its final size and settling. A button press depresses 1-2 px before the action fires. These cues are pre-conscious; users feel the quality without naming it.
- **Robustness > polish.** Every interaction must work the first time, every time. A delightful animation that fires only 80% of the time is worse than no animation. Test the edges: long text, RTL, slow network, no JS, screen reader, reduced motion.
- **Error states are interactions too.** A failed save shows what failed and how to recover, in the same line as the action. Modals that say "An error occurred" with an OK button are user abandonment.

### Edge cases that separate pro tools from toys

- Long values in narrow columns: tooltip on hover, `text-overflow: ellipsis` always.
- Empty state for every list. "No traffic captured yet — start the proxy and load a page" beats a blank rectangle.
- Loading state for every async action. A button that does nothing for 2 seconds after click is broken.
- Network failure: retry with backoff, surface the failure once, don't spam.
- Stale data: mark it as stale ("Last refreshed 12 m ago") rather than silently aging in place.
- Many-tab / many-window state: one tab's actions shouldn't corrupt another's view of the world. `BroadcastChannel` or `storage` events when state must sync.

### The 30-second test

Show the screen to someone who has never used the tool. Within 30 seconds they should be able to answer:

1. What is this thing?
2. What's the most important thing on this screen?
3. What's the next action they're expected to take?

If they can't, the hierarchy is wrong, the labels are wrong, or both. Fix.

---

## Part IV — Working rhythm

### The TDD-light loop

For non-trivial Go work:
1. Write a table-driven test for the smallest case.
2. Make it pass with the most boring code possible.
3. Add a row for the next case. Watch it fail. Make it pass.
4. Refactor with the tests as the safety net.
5. Stop when the rows cover the cases you actually care about. Don't chase 100%.

For UI work:
1. Build the markup first. The page should make sense with CSS turned off.
2. Style the static page until it looks finished without any interactivity.
3. Add the JavaScript. Each handler is one function that does one thing.
4. Test by hand. Then test on slow network (DevTools throttling). Then test with keyboard only.

### Commit discipline

- One logical change per commit. The diff explains itself.
- Imperative mood: "Add", "Fix", "Refactor", not "Added", "Fixes".
- First line ≤ 72 chars, optionally a blank line then a body explaining *why* (not *what* — the diff shows what).
- Never `--no-verify` to bypass hooks. If a hook fails, the hook is right or the hook is wrong; debate that before bypassing.

### Dependencies

- Every dependency is a vote for someone else's bug surface. Add slowly.
- For Go: prefer the standard library. `log/slog`, `net/http`, `database/sql`, `encoding/json` are good enough for almost everything.
- For web: prefer the platform. `fetch`, `URLSearchParams`, `IntersectionObserver`, `crypto.subtle` cover most needs without a single npm install.
- A library is justified when (a) it solves a problem you'd take >1 week to solve correctly, (b) it has more than one maintainer, (c) it has been at version ≥ 1.0 for at least a year. Lower the bar if you're prototyping; raise it for prod.

### When to break the rules

- A user explicitly asking for something this skill discourages: do it, voice the trade-off once, move on.
- A prototype meant to test a hypothesis: skip the discipline, but know you're skipping it.
- A 10-line one-shot script: no test, no error wrapping, no observability. The point is throwaway.

The rules exist to make the *next person*'s life easier. If the next person is no one (truly), the rules are negotiable. If the next person is real, the rules are the gift you leave them.

---

## Sources

This skill stands on the shoulders of:

- Dave Cheney — [Practical Go](https://dave.cheney.net/practical-go), [The Zen of Go](https://changelog.com/gotime/122)
- Peter Bourgon — [Go for Industrial Programming](https://peter.bourgon.org/go-for-industrial-programming/), [Go: Best Practices for Production Environments](https://peter.bourgon.org/go-in-production/)
- Mat Ryer — [How I write HTTP services in Go after 13 years](https://grafana.com/blog/how-i-write-http-services-in-go-after-13-years/)
- Rob Pike — [Proverbs](https://go-proverbs.github.io/) and the Go talks
- Adam Wathan & Steve Schoger — [Refactoring UI](https://refactoringui.com/)
- Rauno Freiberg — [Devouring Details](https://devouringdetails.com/), [Invisible Details of Interaction Design](https://every.to/p/invisible-details-of-interaction-design)
- Linear — [How we redesigned the Linear UI](https://linear.app/now/how-we-redesigned-the-linear-ui)
- Patterns.dev — [Modern web patterns](https://www.patterns.dev/)
