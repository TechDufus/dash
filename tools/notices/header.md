# Third-party notices

dash is licensed under the MIT License (see [LICENSE](LICENSE)). It includes, or is built from,
the third-party software below, under the licences shown.

`make generate` writes this file from tools/notices/header.md (this introduction and the vendored
components, written by hand), go-licenses (the Go modules) and web/scripts/licenses.mjs (the npm
packages); do not edit it directly. `make check` fails when it is out of date, and when a
dependency's licence is outside the policy.

## Vendored components

### shadcn/ui

The components in `web/src/components/ui` were generated from
[shadcn/ui](https://github.com/shadcn-ui/ui) and then changed, and the stylesheet includes
`tailwind.css` from the `shadcn` npm package (a devDependency). shadcn/ui is under the MIT
License:

```text
MIT License

Copyright (c) 2023 shadcn

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### Magic UI

The components in `web/src/components/magicui` were copied from
[Magic UI](https://github.com/magicuidesign/magicui). Magic UI is under the MIT License
(https://github.com/magicuidesign/magicui/blob/main/LICENSE.md):

```text
MIT License

Copyright (c) Magic UI

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### Tailwind CSS

The stylesheet is built with [Tailwind CSS](https://github.com/tailwindlabs/tailwindcss), a build
tool (a devDependency) whose base styles (preflight) and generated utilities ship in the web
assets. Tailwind CSS is under the MIT License:

```text
MIT License

Copyright (c) Tailwind Labs, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### react-remove-scroll-bar

The npm package `react-remove-scroll-bar` (a runtime dependency, listed in the npm section below)
ships no licence file. Its metadata declares MIT, and its repository
(https://github.com/theKashey/react-remove-scroll-bar/blob/master/LICENSE) has this text:

```text
MIT License

Copyright (c) 2025 Anton Korzunov <thekashey@gmail.com>

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### Fonts

The Inter and JetBrains Mono typefaces come from the npm packages `@fontsource-variable/inter` and
`@fontsource-variable/jetbrains-mono`, and their font files are bundled into the web assets. Both
are under the SIL Open Font License 1.1 (OFL-1.1); the licence texts, with the fonts' copyright
lines, are in the npm section below.

### dashboard-icons

dash bundles no icons. The optional `ui.iconBaseURL` setting can point at a copy of
[dashboard-icons](https://github.com/homarr-labs/dashboard-icons), such as its jsDelivr CDN, and the
browser then loads icons from there. dashboard-icons is under the Apache License 2.0, Copyright (c)
2024 Bjorn Lammers, Meier Lukas, Thomas Camlong and Homarr Labs
(https://github.com/homarr-labs/dashboard-icons/blob/main/LICENSE). The logos are trademarks of
their respective owners.

