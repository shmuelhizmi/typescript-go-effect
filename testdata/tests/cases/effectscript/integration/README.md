# Integration check

`quickstart.ets` exercises every implemented construct against the **real**
`effect` npm package. Validated manually (requires network for `npm install`):

```sh
mkdir /tmp/runtest && cd /tmp/runtest && npm i effect
cp quickstart.ets main.ets   # + tsconfig with module nodenext, strict
tsgo -p . && node out/main.js
```

Expected output (modulo timestamp):

```
timestamp=... level=INFO fiber=#0 message=finalized
Hello, user-42! | Hello, missing!! | Hello, forked! | seven | nf:x | 42
```

which demonstrates: defer finalizers via Effect.scoped, service/layer wiring,
catch-arm recovery (taken and not taken), par, fork/join, value- and tag-mode
match, the |> pipeline, and @withSpan decorator lowering.
