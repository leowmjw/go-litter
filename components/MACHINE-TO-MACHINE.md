# Machine to Machine 

## Scenario

- Main App golitter will call the status component
- First build the status component stub.  If more than one deps; build the stub for them (e.g. component-three)
  ```
  $ ../gcloud-cli stubgen build --source-wit-root components/status/wit --dest-wasm target/stub/status/stub.wasm --dest-wit-root target/stub/status/wit
  $ ../gcloud-cli stubgen build --source-wit-root components/component-three/wit --dest-wasm target/stub/component-three/stub.wasm --dest-wit-root target/stub/component-three/wit
  ```
- Next add the stub dependency for each deps
  ```
  $ ../gcloud-cli stubgen add-stub-dependency --overwrite --stub-wit-root target/stub/status/wit --dest-wit-root ./wit
  $ ../gcloud-cli stubgen add-stub-dependency --overwrite --stub-wit-root target/stub/component-three/wit --dest-wit-root ./wit
  ```
  - Build the main app
  ```
  $ tinygo build -target=wasi -tags=purego -o app.module.wasm main.go
  $ make 
  ```

  - Compose the dep to the main app
    ```
    $ ../gcloud-cli stubgen compose --source-wasm app.wasm --stub-wasm target/stub/status/stub.wasm --dest-wasm app.wasm

    $ ../gcloud-cli stubgen compose --source-wasm app.wasm --stub-wasm target/stub/component-three/stub.wasm --dest-wasm app.wasm

    ```

  - Re4s of it 
    ```
8553  ../gcloud-cli  stubgen compose --source-wasm target/build/component-one/component.wasm --stub-wasm target/stub/component-two/stub.wasm --dest-wasm target/build/component-one/compose-1-component-two.wasm\n
 8554  ../gcloud-cli stubgen compose --source-wasm target/build/component-one/compose-1-component-two.wasm --stub-wasm target/stub/component-three/stub.wasm --dest-wasm target/components/component-one.wasm
 8555  mkdir -p target/components
 8556  ../gcloud-cli stubgen compose --source-wasm target/build/component-one/compose-1-component-two.wasm --stub-wasm target/stub/component-three/stub.wasm --dest-wasm target/components/component-one.wasm

    ```