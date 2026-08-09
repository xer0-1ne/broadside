# Broadside

A self-hosted blog that stores your writing as markdown files in a folder.

One binary, no database, no build step, no plugins. Write a post and it appears.
Copy the binary and your content folder to another machine and the site comes
back up exactly as it was.

The name comes from the printed broadside: a single sheet, printed on one side,
posted somewhere public for anyone to read. That is the whole product.

## Status

Under active development and not yet released. The public API and the on-disk
format may still change without notice.

## Why you might want this

- **Your content is just files.** Markdown with YAML frontmatter, in dated
  folders. Edit them in any text editor, sync them with Syncthing, back them up
  with `rsync`, or keep them in git. Broadside watches the folder, so anything
  added, edited, or removed from outside appears without a restart. Settings
  edited by hand are picked up the same way.
- **Nothing to regenerate.** Posts are rendered on request from an in-memory
  index, so publishing is immediate. There is no static build to wait on and no
  cache to clear.
- **One process, about 8MB of memory.** Measured in a container serving pages,
  not estimated. It runs comfortably on the cheapest VPS tier or on a Raspberry
  Pi sitting in a closet.
- **It works without JavaScript.** The site is server-rendered HTML. Scripts
  add the lightbox, the in-page search, and infinite scroll, but every one of
  those degrades to a plain link or a form submission.
- **One page.** Posts appear in full on the timeline, and search filters that
  same page rather than sending you somewhere else. There is nothing to click
  through before you are reading.
- **You can leave.** The exit path is copying a folder. There is no export step
  because there is nothing to export from.

## What it deliberately does not do

Broadside is not a CMS and is not trying to become one. There is no page
builder, no plugin marketplace, no theme templating language, no comment system,
no newsletter, and no membership billing. Each of those is a real product on its
own, and bolting them on is how simple software stops being simple.

Theming is six colors plus a typeface for titles and one for body text, stored
in the config file. If you need more than that, you probably want a different
tool, and that is a fine outcome.

There is no RSS feed. A JSON Feed is served at `/feed.json`, which is what
automation should be reading anyway.

The fonts ship inside the binary rather than being pulled from Google. That
keeps the content security policy strict, avoids telling a third party the
address of everyone who reads your site, and means the page renders correctly on
a machine with no outbound network access. Seven families are embedded, and a
visitor only downloads the two you actually chose.

## The three repositories

Broadside is split into three, each its own repository:

| | |
|---|---|
| **src** | this one. The server: the Go source, the templates, the stylesheet, and the Dockerfile that builds the image. |
| **app** | the iOS client, a native SwiftUI app that talks to the API below. |
| **docker** | how to run it. A compose file, an example environment, and the deployment notes. |

Clone them side by side and the paths in each repository's documentation line
up:

```
broadside/
├── src/
├── app/
└── docker/
```

## Building

```bash
go build ./cmd/broadside
./broadside --site ./site
```

Go 1.25 or newer. There are no code generation steps and no bundler; the
stylesheet is compiled ahead of time and committed, so a clone builds with
nothing else installed. See `web/README.md` if you need to change the CSS.

## Running it in Docker

The Dockerfile is here because this is where the source is. Building the image
is one command from the root of this repository:

```bash
docker build --build-arg VERSION=$(git describe --tags --always) -t broadside:latest .
```

The image is `FROM scratch`: one statically linked binary and nothing else. No
shell, no package manager, no libc. About fifteen megabytes, with nothing in it
to patch.

The build stage is pinned to the machine doing the building and cross-compiles
with `GOARCH`, so building for another architecture costs seconds rather than
emulating a Go compiler under QEMU for minutes. `linux/amd64` and `linux/arm64`
together take about six seconds.

Running it, putting it behind a reverse proxy, the user id it needs on Unraid,
and the body size limits that matter for large uploads are all in the **docker**
repository, because they are about operating Broadside rather than building it.

## Releasing

`.forgejo/workflows/publish.yml` builds the image for `linux/amd64` and
`linux/arm64` and pushes it to Docker Hub. It runs the tests first, and nothing
is published if they fail.

Tag a release and it publishes three tags:

```bash
git tag v1.2.0
git push origin v1.2.0
```

gives `:1.2.0`, `:1.2`, and `:latest`. A push to `main` publishes `:edge` and
nothing else, so `latest` only ever moves when a release is tagged. That
asymmetry is deliberate: the compose file people run defaults them to `latest`,
and a commit that was not meant as a release should not arrive on their server.

Before the first run, set these on the repository in Forgejo, under Settings
then Actions:

| | |
|---|---|
| `DOCKERHUB_USERNAME` | secret. Your Docker Hub account. |
| `DOCKERHUB_TOKEN` | secret. An access token with Read & Write, not your password. |
| `DOCKERHUB_IMAGE` | variable, optional. Defaults to `<username>/broadside`. |

It also needs a runner registered against the repository, with a Docker daemon
available to the publish job. The workflow's header comment says what to check
if it does not start or fails at the build step.

The workflow lives in `.forgejo/workflows/` rather than `.github/workflows/`
because this repository is mirrored to GitHub, and a workflow in the GitHub path
would run there too and publish the same tags twice.

## How your content is laid out

```
site/
├── posts/
│   └── 2026/08/08/01-first-post.md
├── uploads/
│   └── 2026/08/08/01-diagram.png
└── core/
    ├── config.json
    ├── auth.json
    ├── cache/
    └── revisions/
```

Everything adjustable lives in `config.json`, theme included. Two directories
therefore cover a whole site: `core/` for settings, and `posts/` plus
`uploads/` for what you wrote.

Posts and uploads share one naming scheme: `YYYY/MM/DD/NN-slug.ext`. Sorting
those paths alphabetically happens to sort them chronologically, so a directory
listing is already in timeline order. Splitting by date also keeps any single
directory small enough that filesystem performance never becomes something you
have to think about.

The binary lives wherever you put it and is pointed at the site folder with a
flag. It is never inside that folder, which means upgrading is replacing a single
file and your content is never in the blast radius.

## Post format

```markdown
---
title: First Light on the SV503
slug: first-light-on-the-sv503
published: 2026-08-08T14:30:22-05:00
tags: [astro, sv503]
---

Body text in plain markdown.
```

`title`, `slug`, and `published` are required. `updated`, `draft`, `tags`,
`summary`, and `cover` are optional. Any other keys you add are left alone and
written back exactly as you wrote them, so Broadside will never quietly discard
something it did not expect.

Set `draft: true` to keep a post out of the public timeline. Set `published` to a
future time and the post stays hidden until that moment arrives.

### Galleries and other blocks

Markdown has no syntax for a video, an attachment, or a set of photographs shown
together, so those four are written as container directives:

```markdown
:::gallery{caption="Five plates, north up"}
![First plate](/uploads/2026/08/08/01-plate.png "The opening frame")
![Second plate](/uploads/2026/08/08/02-plate.png)
:::

:::video{src="/uploads/2026/08/08/03-clip.mp4"}
:::

:::file{src="/uploads/2026/08/08/04-spec.pdf" name="spec.pdf"}
:::

:::embed{url="https://example.com" title="Example"}
:::
```

A gallery renders as a carousel that wraps at both ends, with arrows, dots, and
a lightbox. Its images are written on their own lines rather than packed into an
attribute, so the file stays readable once there are a dozen of them, and
deleting the two directive lines leaves a post that still shows the same
photographs in the same order. One image in a gallery renders as a plain figure,
because a carousel with a single slide is just a picture with arrows.

Nothing here has to be typed by hand. The editor produces all four, and adding a
second picture to an image block is what turns it into a gallery.

## Contributing

Issues and pull requests are welcome. The one thing worth knowing before opening
a feature request is that the list under "What it deliberately does not do" is a
design position rather than a backlog.

## License

MIT. See [LICENSE](LICENSE).
