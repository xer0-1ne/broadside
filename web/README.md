# Build inputs

Source that is compiled ahead of time and committed, rather than built by
anyone installing Broadside. Nothing in here ships in the binary; the compiled
output does, from `internal/server/static`.

## tailwind/

`input.css` is the source for the stylesheet. Compile it with the standalone
Tailwind CLI, which is a single downloaded binary and needs no Node:

```bash
curl -sL -o web/tailwind/tailwindcss \
  https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-macos-arm64
chmod +x web/tailwind/tailwindcss

./web/tailwind/tailwindcss \
  -i web/tailwind/input.css \
  -o internal/server/static/css/broadside.css \
  --minify
```

Substitute your platform in that URL. The CLI itself is gitignored, since it is
a platform-specific binary of about 100MB; the CSS it produces is committed,
because installing Broadside must never require a build step.
