# Releasing n-netman

Este documento descreve o processo de criação de releases do n-netman.

## Pré-requisitos

- Acesso de push ao repositório
- Todas as mudanças desejadas já commitadas e pushadas para `main`

## Processo de Release

### 1. Verificar que main está atualizado

```bash
git checkout main
git pull origin main
```

### 2. Criar tag semântica

Usamos [Semantic Versioning](https://semver.org/):
- **MAJOR**: mudanças incompatíveis na API/config
- **MINOR**: novas funcionalidades compatíveis
- **PATCH**: correções de bugs

```bash
git tag -a v0.1.0 -m "Release v0.1.0: primeira release pública"
```

### 3. Push da tag

```bash
git push origin v0.1.0
```

O push da tag dispara automaticamente o workflow [release.yaml](.github/workflows/release.yaml).

## O que acontece automaticamente

O GitHub Actions executa o GoReleaser que:

1. **Compila binários** para Linux amd64:
   - `nnetd` (daemon)
   - `nnet` (CLI)

2. **Gera pacotes**:
   - `.deb` para Debian/Ubuntu
   - `.tar.gz` com binários + docs

3. **Publica no GitHub Releases** com:
   - Binários
   - Pacotes
   - Checksums SHA256
   - Changelog automático

## Artefatos Gerados

| Artefato | Descrição |
|----------|-----------|
| `n-netman_X.Y.Z_linux_amd64.tar.gz` | Tarball com binários |
| `n-netman_X.Y.Z_amd64.deb` | Pacote Debian/Ubuntu |
| `checksums.txt` | SHA256 de todos os arquivos |

## Instalação do .deb

```bash
# Download
wget https://github.com/nishisan-dev/n-netman/releases/download/vX.Y.Z/n-netman_X.Y.Z_amd64.deb

# Instalar
sudo dpkg -i n-netman_X.Y.Z_amd64.deb

# Verificar
nnet version
```

## Testar localmente (sem publicar)

```bash
# Instalar GoReleaser
go install github.com/goreleaser/goreleaser/v2@latest

# Validar config
goreleaser check

# Build de teste
goreleaser build --snapshot --clean

# Release completo de teste (local)
goreleaser release --snapshot --clean

# Artefatos em dist/
ls -la dist/
```

## Troubleshooting

### Workflow falhou

1. Verifique os logs em Actions → Release → [run específico]
2. Erros comuns:
   - Tag já existe: delete e recrie
   - Permissões: verifique `permissions: contents: write` no workflow

### Recriar uma release

```bash
# Deletar tag local e remota
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0

# Recriar
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```
