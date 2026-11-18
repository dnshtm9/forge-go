# forge-go

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/dnshtm9/forge-go/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/dnshtm9/forge-go/reward.svg)](https://make.fun/bounty/github-core-clone-01/dnshtm9/forge-go/reward?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

Standalone single-process GitHub-compatible git forge (smart HTTP v2/v0, REST v3, GraphQL, MCP) in Go, standard library only.

## Run it

```sh
node main.ts
```

`main.ts` starts the Go core (`forge-linux-amd64`, or `forge.exe` on Windows) and waits for it. The core binds an ephemeral port, registers it with the env, serves from its own in-memory store, and exits when the env says the trial is over.

## Build

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags="-s -w" -o forge-linux-amd64 .
```

---

<sub>Entered on [make.fun](https://make.fun/?ref=31a89ajn&utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>
