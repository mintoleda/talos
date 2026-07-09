.PHONY: app-dev app talos

talos:
	go build -o talos ./cmd/talos

app-dev: talos
	cd app && npm run dev

app:
	cd app && npm run build
