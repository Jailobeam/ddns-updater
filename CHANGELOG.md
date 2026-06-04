# Changelog

## v2.10.0-jailobeam1 - 2026-06-04

First fork release rebased on upstream `v2.10.0`.

### Added

- Integrated admin UI in the existing web interface
- Browser-based add, edit, delete, activate and deactivate actions for DDNS entries
- Provider-aware forms generated from the embedded upstream provider documentation
- Cloudflare-focused guided setup in the UI
- Manual refresh and `Run update now` controls
- Admin login/logout flow with password change support
- Compact combined overview for entries, status and actions

### Changed

- Reworked the web UI for a more compact self-hosted admin workflow
- Added anonymized UI preview image to the repository documentation

### Fixed

- Unlocked admin mode now correctly re-enables saving in the configuration editor
- Provider guide parsing now handles alternative authentication and mode groups more reliably
- Optional fields documented inside compulsory sections are no longer forced as required inputs
- OVH and Spdyn setup modes are now represented as proper mutually exclusive form choices
- Field descriptions, provider notes, and choice labels were tightened to match the current UI
