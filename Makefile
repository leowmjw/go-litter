.PHONY: compose build bindings compile clean

compose: build
	@../gcloud-cli stubgen compose --source-wasm app.wasm --stub-wasm target/stub/component-three/stub.wasm --dest-wasm app.wasm

build: compile
	wasm-tools component embed ./wit app.module.wasm --output app.embed.wasm
	wasm-tools component new app.embed.wasm -o app.wasm --adapt adapters/tier1/wasi_snapshot_preview1.wasm

bindings:
	wit-bindgen tiny-go --world app --out-dir app ./wit

compile: bindings
	tinygo build -target=wasi -tags=purego -o app.module.wasm main.go

clean:
	rm -rf app
	rm *.wasm
