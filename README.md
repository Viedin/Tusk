# Tusk

A lightweight PostgreSQL and SQLite browser and editor for macOS.

- Browse tables and run SQL with autocomplete and query tabs.
- Edit cells, remove rows, and export results as CSV.
- Save connections with credentials protected by macOS Keychain.

Early development.

## Download

[Download Tusk for macOS](https://github.com/Viedin/Tusk/releases/latest/download/Tusk-macOS-universal.zip) (macOS 13+, Apple silicon and Intel).

Unzip and move `Tusk.app` to Applications. This build isn't notarized; if macOS blocks it, follow [Apple's first-launch instructions](https://support.apple.com/en-us/102445).

## Build

Requires macOS 13+, Xcode Command Line Tools, Go 1.26+, and Node.js 22.12+ or 24+.

```sh
git clone https://github.com/Viedin/Tusk.git
cd Tusk
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0
cd frontend
npm ci
cd ..
wails3 package
open bin/Tusk.app
```

Use `wails3 dev` for development with hot reload.

## License

[MIT](LICENSE). Bundled fonts use the [SIL Open Font License](frontend/public/font-licenses/).
