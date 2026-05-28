package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// gomusicTheme is a dark theme tuned for a music-player vibe:
// near-black background, bright green accent, soft gray text.
type gomusicTheme struct{}

var _ fyne.Theme = (*gomusicTheme)(nil)

var (
	colorBg         = color.NRGBA{R: 0x0e, G: 0x0e, B: 0x10, A: 0xff}
	colorBgElevated = color.NRGBA{R: 0x18, G: 0x18, B: 0x1c, A: 0xff}
	colorBgInput    = color.NRGBA{R: 0x22, G: 0x22, B: 0x28, A: 0xff}
	colorAccent     = color.NRGBA{R: 0x1d, G: 0xb9, B: 0x54, A: 0xff}
	colorAccentDim  = color.NRGBA{R: 0x14, G: 0x83, B: 0x3b, A: 0xff}
	colorText       = color.NRGBA{R: 0xe8, G: 0xe8, B: 0xea, A: 0xff}
	colorTextDim    = color.NRGBA{R: 0x9a, G: 0x9a, B: 0xa2, A: 0xff}
	colorSeparator  = color.NRGBA{R: 0x2a, G: 0x2a, B: 0x30, A: 0xff}
)

func (gomusicTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colorBg
	case theme.ColorNameForeground, theme.ColorNameForegroundOnPrimary:
		return colorText
	case theme.ColorNamePrimary:
		return colorAccent
	case theme.ColorNameHover:
		return colorBgInput
	case theme.ColorNameFocus:
		return colorAccent
	case theme.ColorNameSelection:
		return colorAccentDim
	case theme.ColorNameInputBackground, theme.ColorNameInputBorder:
		return colorBgInput
	case theme.ColorNameButton:
		return colorBgElevated
	case theme.ColorNameDisabled:
		return colorTextDim
	case theme.ColorNamePlaceHolder:
		return colorTextDim
	case theme.ColorNameSeparator:
		return colorSeparator
	case theme.ColorNameShadow:
		return color.NRGBA{0, 0, 0, 0x66}
	case theme.ColorNameScrollBar:
		return colorSeparator
	case theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground:
		return colorBgElevated
	}
	return theme.DarkTheme().Color(name, theme.VariantDark)
}

func (gomusicTheme) Font(s fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(s)
}

func (gomusicTheme) Icon(n fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(n)
}

func (gomusicTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 8
	case theme.SizeNameText:
		return 13
	case theme.SizeNameHeadingText:
		return 22
	case theme.SizeNameSubHeadingText:
		return 16
	}
	return theme.DefaultTheme().Size(n)
}
