# Third-party notices

bqui embeds the following third-party assets in its binary. They are vendored
in this repository under `internal/editor/web` (gzip-compressed) and refreshed
only by maintainers with `make update-assets` (`tools/fetchassets`), which
verifies each release against a pinned digest. Builds never download them. The
full license texts are vendored alongside the assets.

## Monaco Editor 0.57.0

- Source: https://github.com/microsoft/monaco-editor (npm `monaco-editor`)
- License: MIT, Copyright (c) Microsoft Corporation
- Full text: `internal/editor/web/monaco/LICENSE`; bundled third-party
  components are listed in `internal/editor/web/monaco/ThirdPartyNotices.txt`

## FiraCode Nerd Font Mono (Nerd Fonts v3.5.1)

- Fira Code: Copyright (c) 2014, The Fira Code Project Authors
  (https://github.com/tonsky/FiraCode), licensed under the
  SIL Open Font License, Version 1.1. Full text: `internal/editor/web/fonts/LICENSE`.
- Patched by Nerd Fonts (https://github.com/ryanoasis/nerd-fonts), whose
  patcher is MIT licensed. The added icon glyphs keep their upstream
  licenses (including MIT, Apache 2.0, CC BY 4.0 and SIL OFL 1.1); the
  per-icon-set table is in `internal/editor/web/fonts/NERD_FONTS_README.md`.
- The font is used unmodified and embedded only for rendering the editor,
  under a private CSS family name. It is not sold separately.

## Go modules

Go dependencies are listed in `go.mod`; their licenses are distributed with
their source (see `go mod download -json` for module locations).
