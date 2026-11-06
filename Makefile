.PHONY: talos desktop desktop-check desktop-test desktop-accept

talos:
	go build -o talos ./cmd/talos

# Native Rust desktop app (egui) and its local daemon/CLI.
desktop: talos
	cd desktop && cargo run -p talos-desktop

desktop-check:
	cd desktop && cargo check --workspace

desktop-test:
	cd desktop && cargo test --workspace

desktop-accept:
	cd desktop && cargo build --locked -p talosd -p talosdctl -p talos-acp --bins
	cd desktop && python3 scripts/talosd-smoke.py --verbose --timeout 300
