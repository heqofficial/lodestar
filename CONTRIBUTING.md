# Contributing to Lodestar

Lodestar is a small project with a big responsibility: it moves your family's locations from a surveillance business model to one you control. Contributions of any size are welcome.

## Ground rules

1. **Privacy is not a feature, it's the spec.** No change may make plaintext locations, places, or messages visible to the server or to non-members. New features must work with end-to-end encryption.
2. **No tracking. Ever.** No analytics SDKs, no crash-reporting services, no ad frameworks, no third-party data processors.
3. **Consent-first UX.** Tracking features must always have a visible, working "pause" control for the tracked member.
4. **Battery matters.** Background location changes should be validated against the battery budget (~5%/day target).
5. **Tests travel with code.** Server changes need `go test` coverage; app logic should have widget/unit tests where feasible.

## Getting started

```bash
# Server
cd server && go test ./...

# App
cd apps/lodestar
flutter pub get
flutter analyze
flutter test
```

## Where to start

- `docs/threat-model.md` — read before touching crypto or the API.
- `docs/architecture.md` — the envelope protocol and module map.
- Good first issues: UI polish, documentation, tests, F-Droid metadata, translations.

## Pull requests

- Keep PRs small and focused. One change, one PR.
- Run the full test suite and `flutter analyze` before submitting.
- Update docs when the wire protocol or deployment story changes.
- Sign your commits if you can.

## Audits

We want independent security audits. If you're an auditor and want to review the crypto or protocol, open an issue — we'll help you get oriented, and we'll publish your report with attribution.

## License

By contributing you agree that your work is licensed under AGPL-3.0, matching the project.