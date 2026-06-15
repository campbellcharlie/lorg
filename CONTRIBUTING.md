
# Contributing

Top 3 ways to contribute

- `Security`: Find and report security bugs/issues in lorg.
- `Ideation`: You can help by suggesting new features or joining active discussions on [discord](https://discord.gg/J4VPhZqnUu)
- `Contributing`: By testing feature or adding new feature to code.

### Contributing to code
A separate developer interface is available at `grx/dev/` for contributors to test and build backend features ([#36](https://github.com/campbellcharlie/lorg/issues/36)).

Use this interface to build and test backend features — the UI for accepted contributions will be added to the main frontend in subsequent releases.

```bash
cd grx/dev
npm install
npm run dev

# use sudo if you have too
```

But please refrain from creating PRs for new features without first discussing the implementation details.

### Versioning

lorg uses **CalVer**: `YYYY.M.D`, where the number is the **release date** (e.g.
`2026.6.14`). There is no SemVer major/minor — the version is purely "when was
this cut."

When a change ships a functional difference (new/changed behavior, a release,
or a migration), bump the version in **both** places in the same commit:

- `VERSION` — the canonical string (prefixed `v`, e.g. `v2026.6.14`).
- `lrx/version/version.go` — the five constants (`CURRENT_*` and `RELEASED_*`).

Keep the two in sync; a mismatch is a bug. Pure docs/typo commits don't need a
bump. Set the date to the day the change is cut, not the day work started.

### AI Contributions

Use of AI is recommended but make sure you know what code actually does.

### Frontend directory is private ([#36](https://github.com/campbellcharlie/lorg/issues/36))

I have kept the `frontend` directory private for now. Contributing directly to the frontend wouldn't be ideal, as AI lacks good design sense and we want to maintain product quality, experience, and the direction we're moving forward in — plus the frontend is messy.
