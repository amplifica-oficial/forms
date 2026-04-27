# cert-ninja Design System

## Quando usar esta skill

Antes de criar novos componentes, páginas do dashboard, ou modificar estilos globais. Consulte este documento para garantir consistência visual com o design system estabelecido.

---

## Hierarquia de Superfícies (Light Mode)

```
bg-background (210 40% 97%)   ← chão do app (body, SidebarInset)
  ├── bg-sidebar (branco)      ← painel lateral
  └── bg-card (branco)         ← cards, header do dashboard, popovers
        └── bg-muted / bg-muted/50  ← sub-superfícies (tabs, inputs, seções internas)
```

**Regra prática:**
- `bg-background` → área de conteúdo principal, nunca em cards
- `bg-card` → qualquer "caixa" que flutua sobre o fundo
- `bg-muted` ou `bg-muted/50` → destaques dentro de um card, separadores visuais
- `bg-sidebar` → exclusivo do painel lateral

---

## Hierarquia de Superfícies (Dark Mode)

Dark mode usa navy como base (`221 39% 11%`). Não alterar.

```
--background: 221 39% 11%    ← navy (todas as superfícies compartilham o mesmo valor)
--muted: 215 24% 39%         ← slate blue para estados secundários
```

---

## Identidade Visual — Vermelho da Marca

O cert.ninja usa `red-500` como cor de acento principal, espelhando a home page.

### Padrão de ícone (igual à home)
```tsx
<div className="rounded-lg bg-red-500/10 p-1.5">
  <IconName className="h-4 w-4 text-red-500" />
</div>
```
Para cards maiores (feature cards, seções), use `p-2` e `h-5 w-5`.

### Onde usar vermelho
| Elemento | Classe |
|---|---|
| Ícones de métricas em cards | `bg-red-500/10` + `text-red-500` |
| Labels e destaques de texto | `text-red-500` |
| Bordas de hover em cards | `hover:border-red-500/20` |
| Botão primário de ação | `variant="destructive"` |
| Item ativo da sidebar | `from-red-500 to-red-600` (gradient) |
| Badges de destaque | `bg-red-500/10 text-red-500` |

### Onde NÃO usar vermelho
- Estados de erro (use `text-destructive`, `bg-destructive/10`, `border-destructive/40`)
- Texto de corpo genérico (use `text-foreground` ou `text-muted-foreground`)
- Backgrounds de página (use tokens semânticos)

---

## Blobs Decorativos (permitidos hardcoded)

Aplicados na área de conteúdo do dashboard via `pointer-events-none absolute`:
```tsx
<div className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_at_top_right,rgba(239,68,68,0.06),transparent_40%),radial-gradient(ellipse_at_bottom_left,rgba(59,130,246,0.06),transparent_40%)]" />
```
- `rgba(239,68,68,0.06)` = red-500 a 6% — blob superior direito
- `rgba(59,130,246,0.06)` = blue-500 a 6% — blob inferior esquerdo

---

## Tokens de Texto

| Token | Uso |
|---|---|
| `text-foreground` | Títulos, texto principal |
| `text-muted-foreground` | Descrições, subtítulos, labels secundários |
| `text-red-500` | Acento de marca, destaques |
| `text-destructive` | Erros, alertas críticos |

---

## Gráficos (Recharts)

Nunca hardcode cores em gráficos. Use sempre tokens CSS:
- Eixos (stroke): `"hsl(var(--muted-foreground))"`
- Barras/linhas: `"hsl(var(--chart-1))"` a `"hsl(var(--chart-5))"`
- Config do ChartContainer: `color: "hsl(var(--chart-N))"`

---

## Cores Hardcoded Proibidas

Substituir sempre que encontrar:
| Hardcoded | Substituição |
|---|---|
| `#64748b` | `hsl(var(--muted-foreground))` |
| `#22c55e` | `hsl(var(--chart-2))` |
| `bg-red-900` (estado de erro) | `bg-destructive/10 border-destructive/40` |
| Gradiente sidebar antigo `#217bfe…#ac87eb` | `from-red-500 to-red-600` |
