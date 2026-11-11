.PHONY: app-dev app talos desktop desktop-check desktop-test desktop-accept

talos:
	go build -o talos ./cmd/talos

app-dev: talos
	cd app && npm run dev

app:
	cd app && npm run build

# Native GPUI desktop app and its local daemon/CLI.
desktop: talos
	cd desktop && RUST_FONTCONFIG_DLOPEN=1 cargo run -p talos-gpui

desktop-check:
	cd desktop && RUST_FONTCONFIG_DLOPEN=1 cargo check --workspace

desktop-test:
	cd desktop && RUST_FONTCONFIG_DLOPEN=1 cargo test --workspace

desktop-accept:
	cd desktop && cargo build --locked -p talosd -p talosdctl -p talos-acp --bins
	cd desktop && python3 scripts/talosd-smoke.py --verbose --timeout 300
