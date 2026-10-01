package main

// Building blocks for the window: colours, text, buttons, inputs, dropdowns,
// cards. Everything is drawn by the app itself (Gio); there is no browser or
// web view anywhere.

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func alpha(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(a * 255)
	return c
}

var (
	colBg     = rgb(0x0c0e12)
	colPanel  = rgb(0x101419)
	colCard   = rgb(0x151920)
	colCard2  = rgb(0x1a1f28)
	colInset  = rgb(0x0e1116)
	colLine   = rgb(0x21262f)
	colLine2  = rgb(0x2b323d)
	colText   = rgb(0xe7e9ee)
	colMuted  = rgb(0x8b93a3)
	colFaint  = rgb(0x5e6676)
	colAccent = rgb(0x6e8bff)
	colAcc2   = rgb(0x9a7bff)
	colGood   = rgb(0x3dd68c)
	colWarn   = rgb(0xf5b94a)
	colBad    = rgb(0xf2706b)
	colSoft   = rgb(0xa7b6ff) // accent text on dark
	colModal  = rgb(0x161a21)
	colToast  = rgb(0x1c212b)
)

func toneColor(tone string) color.NRGBA {
	switch tone {
	case "good":
		return colGood
	case "warn":
		return colWarn
	case "bad":
		return colBad
	case "busy", "info":
		return colSoft
	}
	return colMuted
}

const (
	sans = font.Typeface("Segoe UI Variable Text, Segoe UI, Inter, Go")
	mono = font.Typeface("Cascadia Mono, Cascadia Code, Consolas, Go Mono")
)

func mustIcon(data []byte) *widget.Icon {
	ic, err := widget.NewIcon(data)
	if err != nil {
		panic(err)
	}
	return ic
}

var (
	icPlus    = mustIcon(icons.ContentAdd)
	icSearch  = mustIcon(icons.ActionSearch)
	icCopy    = mustIcon(icons.ContentContentCopy)
	icRefresh = mustIcon(icons.NavigationRefresh)
	icTrash   = mustIcon(icons.ActionDelete)
	icKey     = mustIcon(icons.CommunicationVPNKey)
	icShield  = mustIcon(icons.ActionVerifiedUser)
	icTerm    = mustIcon(icons.HardwareKeyboard)
	icCheck   = mustIcon(icons.NavigationCheck)
	icAlert   = mustIcon(icons.AlertWarning)
	icClose   = mustIcon(icons.NavigationClose)
	icSpark   = mustIcon(icons.ImageFlare)
	icZap     = mustIcon(icons.ImageFlashOn)
	icFolder  = mustIcon(icons.FileFolder)
	icFile    = mustIcon(icons.ActionDescription)
	icChevR   = mustIcon(icons.NavigationChevronRight)
	icChevD   = mustIcon(icons.NavigationExpandMore)
	icInfo    = mustIcon(icons.ActionInfo)
	icDot     = mustIcon(icons.ImageLens)
	icLink    = mustIcon(icons.ContentLink)
	icHeart   = mustIcon(icons.ActionFavorite)
)

// ---------- shapes ----------

func fillRect(gtx C, r image.Rectangle, col color.NRGBA) {
	paint.FillShape(gtx.Ops, col, clip.Rect(r).Op())
}

func fillRRect(gtx C, r image.Rectangle, radius int, col color.NRGBA) {
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(r, radius).Op(gtx.Ops))
}

func strokeRRect(gtx C, r image.Rectangle, radius int, width float32, col color.NRGBA) {
	rr := clip.UniformRRect(r, radius)
	paint.FillShape(gtx.Ops, col, clip.Stroke{Path: rr.Path(gtx.Ops), Width: width}.Op())
}

// gradRRect fills a rounded rectangle with a diagonal two-colour gradient.
func gradRRect(gtx C, r image.Rectangle, radius int, c1, c2 color.NRGBA) {
	defer clip.UniformRRect(r, radius).Push(gtx.Ops).Pop()
	paint.LinearGradientOp{
		Stop1: f32.Pt(float32(r.Min.X), float32(r.Min.Y)), Color1: c1,
		Stop2: f32.Pt(float32(r.Max.X), float32(r.Max.Y)), Color2: c2,
	}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
}

func hsl(h, s, l float64) color.NRGBA {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	return color.NRGBA{R: uint8((r + m) * 255), G: uint8((g + m) * 255), B: uint8((b + m) * 255), A: 255}
}

// box draws a rounded background and border behind a widget.
type box struct {
	bg, border color.NRGBA
	radius     unit.Dp
	in         layout.Inset
}

func (b box) Layout(gtx C, w layout.Widget) D {
	return layout.Background{}.Layout(gtx, func(gtx C) D {
		r := image.Rectangle{Max: gtx.Constraints.Min}
		rad := gtx.Dp(b.radius)
		if b.bg.A > 0 {
			fillRRect(gtx, r, rad, b.bg)
		}
		if b.border.A > 0 {
			strokeRRect(gtx, r, rad, float32(gtx.Dp(1)), b.border)
		}
		return D{Size: r.Max}
	}, func(gtx C) D { return b.in.Layout(gtx, w) })
}

// fixed lays w out in exactly the given width.
func fixed(width int, w layout.Widget) layout.Widget {
	return func(gtx C) D {
		gtx.Constraints.Min.X, gtx.Constraints.Max.X = width, width
		return w(gtx)
	}
}

// vcenter places w in the middle of a fixed-height row, full width.
func vcenter(gtx C, in layout.Inset, w layout.Widget) D {
	size := gtx.Constraints.Min
	macro := op.Record(gtx.Ops)
	cg := gtx
	cg.Constraints.Min = image.Pt(size.X, 0)
	d := in.Layout(cg, w)
	call := macro.Stop()
	st := op.Offset(image.Pt(0, (size.Y-d.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	st.Pop()
	return D{Size: image.Pt(size.X, max(size.Y, d.Size.Y))}
}

func gap(v unit.Dp) layout.FlexChild { return layout.Rigid(layout.Spacer{Width: v, Height: v}.Layout) }

// ---------- text ----------

type textOpt func(*material.LabelStyle)

func bold(l *material.LabelStyle)   { l.Font.Weight = font.SemiBold }
func medium(l *material.LabelStyle) { l.Font.Weight = font.Medium }
func monoFont(l *material.LabelStyle) {
	l.Font.Typeface = mono
}
func oneLine(l *material.LabelStyle) { l.MaxLines = 1 }
func centered(l *material.LabelStyle) {
	l.Alignment = text.Middle
}

func (g *gui) txt(size unit.Sp, s string, col color.NRGBA, opts ...textOpt) material.LabelStyle {
	l := material.Label(g.th, size, s)
	l.Color = col
	for _, o := range opts {
		o(&l)
	}
	return l
}

func (g *gui) icon(gtx C, ic *widget.Icon, size unit.Dp, col color.NRGBA) D {
	px := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(px, px))
	return ic.Layout(gtx, col)
}

func (g *gui) spinner(gtx C, size unit.Dp) D {
	px := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(px, px))
	l := material.Loader(g.th)
	l.Color = colSoft
	return l.Layout(gtx)
}

// ---------- buttons ----------

type btnKind int

const (
	btnPlain btnKind = iota
	btnPrimary
	btnGhost
	btnDanger      // plain, turns red on hover
	btnDangerSolid // red
	btnAI          // the wide sidebar button with the gradient border
)

type btnStyle struct {
	kind  btnKind
	small bool
	icon  *widget.Icon
	label string
	wide  bool // fill the available width
}

func (g *gui) button(gtx C, c *widget.Clickable, s btnStyle) D {
	h, padX, rad, size, iconSize := unit.Dp(34), unit.Dp(14), unit.Dp(9), unit.Sp(14), unit.Dp(16)
	if s.small {
		h, padX, rad, size, iconSize = 29, 11, 8, 12.5, 14
	}
	if s.kind == btnAI {
		h, rad = 40, 10
	}
	if s.label == "" {
		padX = 0
	}
	enabled := gtx.Enabled()
	hov := c.Hovered() && enabled
	bg, border, fg := colCard2, colLine2, colText
	switch s.kind {
	case btnPlain:
		if hov {
			bg, border = rgb(0x212733), rgb(0x3a4250)
		}
	case btnGhost:
		bg, border, fg = color.NRGBA{}, color.NRGBA{}, colMuted
		if hov {
			bg, fg = colCard2, colText
		}
	case btnDanger:
		if hov {
			bg, border, fg = alpha(colBad, .08), alpha(colBad, .35), colBad
		}
	case btnDangerSolid:
		bg, border, fg = rgb(0xc9504b), color.NRGBA{}, rgb(0xffffff)
		if hov {
			bg = rgb(0xd85a55)
		}
	case btnPrimary, btnAI:
		fg = rgb(0xffffff)
	}
	if !enabled {
		fg = alpha(fg, .45)
	}
	return c.Layout(gtx, func(gtx C) D {
		if enabled {
			pointer.CursorPointer.Add(gtx.Ops)
		}
		hp := gtx.Dp(h)
		gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = hp, hp
		if s.label == "" {
			gtx.Constraints.Min.X = hp - gtx.Dp(2)
		}
		if !s.wide {
			gtx.Constraints.Min.X = min(gtx.Constraints.Min.X, hp*8)
		}
		return layout.Background{}.Layout(gtx, func(gtx C) D {
			r := image.Rectangle{Max: gtx.Constraints.Min}
			rp := gtx.Dp(rad)
			switch s.kind {
			case btnPrimary:
				c1, c2 := rgb(0x6e8bff), rgb(0x8a78ff)
				if hov {
					c1, c2 = rgb(0x7f99ff), rgb(0x9b8aff)
				}
				if !enabled {
					c1, c2 = alpha(c1, .45), alpha(c2, .45)
				}
				gradRRect(gtx, r, rp, c1, c2)
			case btnAI:
				gradRRect(gtx, r, rp, colAccent, colAcc2)
				in := r.Inset(gtx.Dp(1))
				inner := colCard2
				if hov {
					inner = rgb(0x202634)
				}
				fillRRect(gtx, in, rp-gtx.Dp(1), inner)
			default:
				if bg.A > 0 {
					fillRRect(gtx, r, rp, bg)
				}
				if border.A > 0 {
					strokeRRect(gtx, r, rp, float32(gtx.Dp(1)), border)
				}
			}
			return D{Size: r.Max}
		}, func(gtx C) D {
			return layout.Center.Layout(gtx, func(gtx C) D {
				return layout.Inset{Left: padX, Right: padX}.Layout(gtx, func(gtx C) D {
					var kids []layout.FlexChild
					if s.icon != nil {
						ic := fg
						if s.kind == btnAI {
							ic = rgb(0xb8a6ff)
						}
						kids = append(kids, layout.Rigid(func(gtx C) D { return g.icon(gtx, s.icon, iconSize, ic) }))
						if s.label != "" {
							kids = append(kids, gap(7))
						}
					}
					if s.label != "" {
						kids = append(kids, layout.Rigid(g.txt(size, s.label, fg, medium, oneLine).Layout))
					}
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
				})
			})
		})
	})
}

// link is a text-only button, as used for the donation link.
func (g *gui) link(gtx C, c *widget.Clickable, ic *widget.Icon, label string) D {
	col := rgb(0xa9b7ff)
	if c.Hovered() {
		col = rgb(0xc9d2ff)
	}
	return c.Layout(gtx, func(gtx C) D {
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return g.icon(gtx, ic, 13, col) }),
			gap(5),
			layout.Rigid(g.txt(11.5, label, col).Layout),
		)
	})
}

// ---------- text input ----------

func (g *gui) input(gtx C, e *widget.Editor, hint string, useMono, disabled bool, lead *widget.Icon) D {
	border := colLine2
	if gtx.Focused(e) && !disabled {
		border = colAccent
	}
	bg := colInset
	if disabled {
		bg = rgb(0x12151b)
		gtx = gtx.Disabled()
	}
	h := gtx.Dp(37)
	gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = h, h
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Background{}.Layout(gtx, func(gtx C) D {
		r := image.Rectangle{Max: gtx.Constraints.Min}
		fillRRect(gtx, r, gtx.Dp(9), bg)
		strokeRRect(gtx, r, gtx.Dp(9), float32(gtx.Dp(1)), border)
		if gtx.Focused(e) && !disabled {
			strokeRRect(gtx, r.Inset(-gtx.Dp(2)), gtx.Dp(11), float32(gtx.Dp(3)), alpha(colAccent, .16))
		}
		return D{Size: r.Max}
	}, func(gtx C) D {
		return vcenter(gtx, layout.Inset{Left: 11, Right: 11}, func(gtx C) D {
			ed := material.Editor(g.th, e, hint)
			ed.TextSize = 14
			ed.Color = colText
			if disabled {
				ed.Color = colMuted
			}
			ed.HintColor = rgb(0x4b5262)
			ed.SelectionColor = alpha(colAccent, .35)
			ed.Font.Typeface = sans
			if useMono {
				ed.Font.Typeface = mono
				ed.TextSize = 13
			}
			var kids []layout.FlexChild
			if lead != nil {
				kids = append(kids, layout.Rigid(func(gtx C) D { return g.icon(gtx, lead, 15, colFaint) }), gap(7))
			}
			kids = append(kids, layout.Flexed(1, ed.Layout))
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
		})
	})
}

// ---------- dropdown ----------

type selectBox struct {
	click widget.Clickable
	items []widget.Clickable
	list  widget.List
	open  bool
	up    bool // open above the box (for the one at the bottom of the sidebar)
}

// selectUI draws a dropdown. It returns the index picked this frame, or -1.
func (g *gui) selectUI(gtx C, s *selectBox, labels []string, current int, useMono, disabled, compact bool) (D, int) {
	picked := -1
	if len(s.items) < len(labels) {
		s.items = make([]widget.Clickable, len(labels))
	}
	if s.click.Clicked(gtx) && !disabled {
		s.open = !s.open
		if s.open {
			g.openSel = s
			// Open upwards when the list would run off the bottom of the window.
			if h := s.click.History(); len(h) > 0 && !compact {
				top := int(g.ptr.Y) - h[len(h)-1].Position.Y
				need := min(len(labels)*gtx.Dp(34)+gtx.Dp(12), gtx.Dp(272))
				s.up = top+gtx.Dp(41)+need > g.winSize.Y && top > need
			}
		} else {
			g.openSel = nil
		}
	}
	if s.open && g.openSel != s {
		s.open = false
	}
	for i := range labels {
		if s.items[i].Clicked(gtx) && s.open {
			picked = i
			s.open = false
			g.openSel = nil
		}
	}
	label := ""
	if current >= 0 && current < len(labels) {
		label = labels[current]
	}
	h, size, bg, border, fg := unit.Dp(37), unit.Sp(14), colInset, colLine2, colText
	if compact {
		h, size, bg, border, fg = 29, 11.5, colCard, colLine, colMuted
	}
	if useMono && !compact {
		size = 13
	}
	if s.open {
		border = colAccent
	}
	if disabled {
		bg, fg = rgb(0x12151b), colMuted
	}
	face := sans
	if useMono {
		face = mono
	}
	dims := s.click.Layout(gtx, func(gtx C) D {
		if !disabled {
			pointer.CursorPointer.Add(gtx.Ops)
		}
		hp := gtx.Dp(h)
		gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = hp, hp
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Background{}.Layout(gtx, func(gtx C) D {
			r := image.Rectangle{Max: gtx.Constraints.Min}
			fillRRect(gtx, r, gtx.Dp(9), bg)
			strokeRRect(gtx, r, gtx.Dp(9), float32(gtx.Dp(1)), border)
			return D{Size: r.Max}
		}, func(gtx C) D {
			return vcenter(gtx, layout.Inset{Left: 11, Right: 8}, func(gtx C) D {
				l := g.txt(size, label, fg, oneLine)
				l.Font.Typeface = face
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, l.Layout),
					layout.Rigid(func(gtx C) D { return g.icon(gtx, icChevD, 18, colMuted) }),
				)
			})
		})
	})
	if s.open {
		// The list is drawn after everything else so it sits on top.
		s.list.Axis = layout.Vertical
		macro := op.Record(gtx.Ops)
		pgtx := gtx
		pgtx.Constraints = layout.Constraints{Min: image.Pt(dims.Size.X, 0), Max: image.Pt(dims.Size.X, gtx.Dp(264))}
		rec := op.Record(gtx.Ops)
		pd := box{bg: rgb(0x171c24), border: colLine2, radius: 10, in: layout.UniformInset(4)}.Layout(pgtx, func(gtx C) D {
			ls := material.List(g.th, &s.list)
			ls.AnchorStrategy = material.Overlay
			ls.Indicator.Color = rgb(0x323a47)
			return ls.Layout(gtx, len(labels), func(gtx C, i int) D {
				it := &s.items[i]
				return it.Layout(gtx, func(gtx C) D {
					pointer.CursorPointer.Add(gtx.Ops)
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					bg := color.NRGBA{}
					if it.Hovered() {
						bg = rgb(0x222a3a)
					} else if i == current {
						bg = rgb(0x1c2230)
					}
					return box{bg: bg, radius: 7, in: layout.Inset{Left: 9, Right: 9, Top: 7, Bottom: 7}}.Layout(gtx, func(gtx C) D {
						l := g.txt(size, labels[i], colText, oneLine)
						l.Font.Typeface = face
						return l.Layout(gtx)
					})
				})
			})
		})
		call := rec.Stop()
		y := dims.Size.Y + gtx.Dp(4)
		if s.up {
			y = -pd.Size.Y - gtx.Dp(4)
		}
		st := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		st.Pop()
		op.Defer(gtx.Ops, macro.Stop())
	}
	return dims, picked
}

// ---------- small pieces ----------

func (g *gui) pill(gtx C, tone, label string, lead layout.Widget) D {
	col := toneColor(tone)
	bg := alpha(col, .12)
	if tone == "muted" || tone == "" {
		bg = rgb(0x1d222c)
	}
	return box{bg: bg, radius: 12, in: layout.Inset{Left: 10, Right: 10, Top: 4, Bottom: 4}}.Layout(gtx, func(gtx C) D {
		var kids []layout.FlexChild
		if lead != nil {
			kids = append(kids, layout.Rigid(lead), gap(6))
		}
		kids = append(kids, layout.Rigid(g.txt(12, label, col, bold, oneLine).Layout))
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx, kids...)
	})
}

// avatar is the coloured letter tile for a server, or a key for a bare host key.
func (g *gui) avatar(gtx C, s *ServerView, size unit.Dp) D {
	px := gtx.Dp(size)
	r := image.Rectangle{Max: image.Pt(px, px)}
	rad := px * 9 / 32
	if s.Kind == "known" {
		fillRRect(gtx, r, rad, rgb(0x1d2330))
		strokeRRect(gtx, r, rad, float32(gtx.Dp(1)), colLine2)
		layout.Stack{Alignment: layout.Center}.Layout(gtx,
			layout.Expanded(func(gtx C) D { return D{Size: r.Max} }),
			layout.Stacked(func(gtx C) D { return g.icon(gtx, icKey, size*15/32, colMuted) }))
		return D{Size: r.Max}
	}
	var h uint32
	for _, c := range s.Alias {
		h = h*31 + uint32(c)
	}
	hue := float64(h % 360)
	gradRRect(gtx, r, rad, hsl(hue, .58, .52), hsl(math.Mod(hue+40, 360), .62, .40))
	letter := "?"
	for _, c := range s.Alias {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			letter = string(c)
			break
		}
	}
	if letter[0] >= 'a' && letter[0] <= 'z' {
		letter = string(letter[0] - 32)
	}
	gtx.Constraints = layout.Exact(r.Max)
	layout.Center.Layout(gtx, g.txt(unit.Sp(float32(size)*0.42), letter, rgb(0xffffff), bold).Layout)
	return D{Size: r.Max}
}

func (g *gui) dot(gtx C, tone string) D {
	px := gtx.Dp(8)
	if tone == "busy" {
		return g.spinner(gtx, 12)
	}
	col := rgb(0x454d5b)
	if tone == "good" || tone == "warn" || tone == "bad" {
		col = toneColor(tone)
		halo := image.Rectangle{Min: image.Pt(-gtx.Dp(3), -gtx.Dp(3)), Max: image.Pt(px+gtx.Dp(3), px+gtx.Dp(3))}
		paint.FillShape(gtx.Ops, alpha(col, .14), clip.Ellipse(halo).Op(gtx.Ops))
	}
	paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rectangle{Max: image.Pt(px, px)}).Op(gtx.Ops))
	return D{Size: image.Pt(px, px)}
}

// stepMark is the round status icon at the start of an Access row.
func (g *gui) stepMark(gtx C, tone string) D {
	px := gtx.Dp(30)
	r := image.Rectangle{Max: image.Pt(px, px)}
	col, bg := colFaint, rgb(0x1d222c)
	ic := icDot
	switch tone {
	case "good":
		col, bg, ic = colGood, alpha(colGood, .13), icCheck
	case "warn":
		col, bg, ic = colWarn, alpha(colWarn, .13), icAlert
	case "bad":
		col, bg, ic = colBad, alpha(colBad, .13), icClose
	case "busy":
		col, bg = colSoft, alpha(colAccent, .12)
	}
	paint.FillShape(gtx.Ops, bg, clip.Ellipse(r).Op(gtx.Ops))
	gtx.Constraints = layout.Exact(r.Max)
	layout.Center.Layout(gtx, func(gtx C) D {
		if tone == "busy" {
			return g.spinner(gtx, 14)
		}
		sz := unit.Dp(16)
		if ic == icDot {
			sz = 8
		}
		return g.icon(gtx, ic, sz, col)
	})
	return D{Size: r.Max}
}

func (g *gui) badge(gtx C, label string, width unit.Dp) D {
	w := gtx.Dp(width)
	gtx.Constraints.Min.X, gtx.Constraints.Max.X = w, w
	return box{bg: rgb(0x1e2431), radius: 6, in: layout.Inset{Top: 4, Bottom: 4}}.Layout(gtx,
		g.txt(10.5, label, rgb(0xaab4c8), bold, centered, oneLine).Layout)
}

// card is the rounded panel used for Access, Connection and host keys.
func (g *gui) card(gtx C, w layout.Widget) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return box{bg: colCard, border: colLine, radius: 14}.Layout(gtx, w)
}

func (g *gui) divider(gtx C) D {
	h := gtx.Dp(1)
	fillRect(gtx, image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, h)}, colLine)
	return D{Size: image.Pt(gtx.Constraints.Max.X, h)}
}
