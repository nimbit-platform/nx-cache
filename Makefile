.PHONY: generate css test e2e build docker tidy screenshots

generate:
	templ generate

css:
	npx @tailwindcss/cli -i assets/css/globals.css -o internal/web/static/app.css --minify

tidy:
	go mod tidy

test: generate
	go test ./...

e2e:
	go test -tags e2e ./e2e/... -count=1 -timeout 3m

screenshots:
	go run ./cmd/screenshot

build: generate css
	go build -o bin/nx-cache ./cmd/server

docker:
	docker build -t nx-cache:local .
