# Changelog

## [0.19.0](https://github.com/buchenberg/yaah/compare/v0.18.0...v0.19.0) (2026-07-21)


### Features

* **agent:** spontaneous delegation, executor self-correction, identity/doc overhaul ([be308b5](https://github.com/buchenberg/yaah/commit/be308b591372710206b04f75bb77fcc9688ff097))
* **agent:** spontaneous delegation, executor self-correction, identity/doc overhaul ([f5da3a6](https://github.com/buchenberg/yaah/commit/f5da3a6f6df51b8d5cd3971979a2100bc576ef33))

## [0.18.0](https://github.com/buchenberg/yaah/compare/v0.17.0...v0.18.0) (2026-07-21)


### Features

* dual-loop execution — inner executor chains tools per outer turn ([deba521](https://github.com/buchenberg/yaah/commit/deba52196412eaf9aaf06de81570be886da1bb24))

## [0.17.0](https://github.com/buchenberg/yaah/compare/v0.16.0...v0.17.0) (2026-07-20)


### Features

* add 'yaah skill create' and 'yaah skill edit' CLI commands ([13faa40](https://github.com/buchenberg/yaah/commit/13faa40ae43b121e2b0cf2eed6a62774aecd59f1))

## [0.16.0](https://github.com/buchenberg/yaah/compare/v0.15.0...v0.16.0) (2026-07-20)


### Features

* add plan tool with CRUD + approval workflow ([3f60250](https://github.com/buchenberg/yaah/commit/3f6025092eb681d2da43dea73e2dd5c5e48b6112))
* **providers:** extract agent session, add configurable provider timeout ([1b58083](https://github.com/buchenberg/yaah/commit/1b5808385a21fbb67f2343f5a357e1a71662faa3))
* **tools:** add http tool for generic REST API calls ([1496c9a](https://github.com/buchenberg/yaah/commit/1496c9a1d36db6564736775aa2cf31f79d38a919))
* **tools:** add replace and json_query tools ([ece1330](https://github.com/buchenberg/yaah/commit/ece13306353f32eed095618d2dc9761eb9d86561))


### Bug Fixes

* **tools:** fix calculate Pratt parser binary operators and right-associativity ([b09d682](https://github.com/buchenberg/yaah/commit/b09d682b752406c18b20c6ca97088d83d76c6914))


### Documentation

* sync tool inventory across identity.md, AGENTS.md, README.md ([a758ed7](https://github.com/buchenberg/yaah/commit/a758ed7547701463aa2672c004e2a542f9f6dc48))
* update tool inventory to include replace, json_query, and http ([b1960d4](https://github.com/buchenberg/yaah/commit/b1960d4ae41f381fdb6cdaf51e8cfe6942bf224b))

## [0.15.0](https://github.com/buchenberg/yaah/compare/v0.14.3...v0.15.0) (2026-07-20)


### Features

* **errorclassify:** structured error classification with provider fallback ([a53f737](https://github.com/buchenberg/yaah/commit/a53f737057d364ba11d5eba7205a7d3701d3f561))
* **errorclassify:** structured error classification with provider fallback ([57b0a53](https://github.com/buchenberg/yaah/commit/57b0a53eee477a2a4f2390e59ed72443948f6d70))


### Bug Fixes

* **errorclassify:** gofmt alignment ([80dfb8f](https://github.com/buchenberg/yaah/commit/80dfb8fc06e91261c27f1d27d22340dd163e50ef))

## [0.14.3](https://github.com/buchenberg/yaah/compare/v0.14.2...v0.14.3) (2026-07-20)


### Bug Fixes

* **ci:** add checkout step to release upload job ([7caa713](https://github.com/buchenberg/yaah/commit/7caa7139a5d3922600abb4fee4f9b70b4ca0aca6))
* **ci:** add checkout to release upload job ([249ca56](https://github.com/buchenberg/yaah/commit/249ca56cc2bd1eaf4f99f5bf390c2150b5344123))

## [0.14.2](https://github.com/buchenberg/yaah/compare/v0.14.1...v0.14.2) (2026-07-20)


### CI / Build

* add cross-compile and upload to release-please workflow ([0165a05](https://github.com/buchenberg/yaah/commit/0165a054e4043f0ce450a0300d965189f338feeb))
* inline build/upload into release-please workflow ([aad2c42](https://github.com/buchenberg/yaah/commit/aad2c4256abf0a1e1e0f07480e036cf9de54336a))

## [0.14.1](https://github.com/buchenberg/yaah/compare/v0.14.0...v0.14.1) (2026-07-20)


### CI / Build

* add .release-please-manifest.json ([cfeb6e7](https://github.com/buchenberg/yaah/commit/cfeb6e7dd56f9d7fc77fd7aca108d982ac255e99))
* add missing .release-please-manifest.json ([9365eb6](https://github.com/buchenberg/yaah/commit/9365eb670a98ae2287c7072262ca84a4d09da7e8))
* add release-please for automated tag and release creation ([1195a73](https://github.com/buchenberg/yaah/commit/1195a7390a54eab66ac74226fe5e98bad4740622))
* add release-please for automated tag and release creation ([a1388f8](https://github.com/buchenberg/yaah/commit/a1388f8459668271383d05d0a7c6c6605897a0e5))
* bump release-please-action to v5 (Node 24) ([97a85b0](https://github.com/buchenberg/yaah/commit/97a85b0484555c1295f77660299e2b115d1c0e8d))
* bump release-please-action to v5 (Node 24) ([3fb78b3](https://github.com/buchenberg/yaah/commit/3fb78b30d600f1105ebb8415441bc56512c4b9de))
