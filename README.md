# vark

Self-hosted YouTube archiver. Go backend (JSON API + embedded React SPA), single SQLite file, no
external services required.

## Run

```bash
cp .env.example .env   # fill in the values
docker compose up --build
```

(`compose.yaml` lands in a later task; for now build the image directly with
`docker build -f backend/Containerfile .`.)

## Develop

```bash
make dev   # backend on 127.0.0.1:8080 + Vite dev server proxying /api
```

See `AGENTS.md` for conventions, locked technical choices, and commands.
