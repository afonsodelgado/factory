# Fonts

`jetbrains-mono-regular.woff2` and `jetbrains-mono-bold.woff2` are subsets of JetBrainsMono Nerd Font (the non-Mono "Nerd Font" variant), Nerd Fonts 3.5.1, patched from JetBrains Mono 2.304. They keep only the characters this site uses: printable ASCII, Latin-1, punctuation, arrows, maths, technical symbols, box drawing, geometric shapes, dingbats and the Nerd Font icons the page draws.

JetBrains Mono is Copyright 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono), licensed under the SIL Open Font License 1.1. The full license is in `OFL.txt`, copied unchanged from the Nerd Fonts release. The subsets keep the fonts' original name records, including the copyright and license.

The Nerd Font icons in the subsets come from Font Awesome (CC BY 4.0, https://github.com/FortAwesome/Font-Awesome), Material Design Icons (Apache 2.0, https://github.com/Templarian/MaterialDesign-Font) and Octicons (MIT, https://github.com/primer/octicons), as packaged by Nerd Fonts (https://github.com/ryanoasis/nerd-fonts).

## Inputs

Download location: https://github.com/ryanoasis/nerd-fonts/tree/v3.5.1/patched-fonts/JetBrainsMono/Ligatures

| File                                | SHA-256                                                            |
| ----------------------------------- | ------------------------------------------------------------------ |
| `JetBrainsMonoNerdFont-Regular.ttf` | `1c680e8cde9fcf8b88a5605ce8d1fb94dd3fb15841f7ca7bf4c55664855e5611` |
| `JetBrainsMonoNerdFont-Bold.ttf`    | `e490660ad75e0b152c93b1604c2ea1a4ea675f1b8a3fe0d005b1998568f99f1f` |

## Regenerate

Builds use the committed files and never regenerate them. To regenerate after changing the page's text or icons, from `apps/web`:

```sh
python3 -m venv /tmp/factory-fonts
/tmp/factory-fonts/bin/pip install -r scripts/font-requirements.txt
PATH=/tmp/factory-fonts/bin:$PATH NERD_FONT_DIR=<directory with both TTF files> pnpm fonts
```

`NERD_FONT_DIR` defaults to `~/Library/Fonts`. The script refuses inputs whose SHA-256 differs, subsets both weights with `pyftsubset --flavor=woff2 --no-hinting`, and fails when a character the page uses is in the source font but missing from a subset. Characters the source font does not have at all are listed; the browser draws them with the fallback font.
