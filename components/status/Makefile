.PHONY: build bindings compile clean

build: compile
	wasm-tools component embed ./wit status.module.wasm --output status.embed.wasm
	wasm-tools component new status.embed.wasm -o status.wasm --adapt adapters/tier1/wasi_snapshot_preview1.wasm

bindings:
	wit-bindgen tiny-go --world status --out-dir binding ./wit

compile: bindings
	tinygo build -target=wasi -tags=purego -o status.module.wasm main.go

clean:
	rm -rf status
	rm *.wasm
