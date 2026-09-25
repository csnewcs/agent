package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

type ComponentsBuilder struct {
	title         string
	subTitle      string
	bodyText      string
	footerText    string
	imageURL      string
	buttons       []discordgo.MessageComponent
	selectMenus   []discordgo.MessageComponent
	rawComponents []discordgo.MessageComponent
}

func NewComponentsBuilder() *ComponentsBuilder {
	return &ComponentsBuilder{}
}

func (b *ComponentsBuilder) WithTitle(title string) *ComponentsBuilder {
	b.title = title
	return b
}

func (b *ComponentsBuilder) WithSubTitle(subTitle string) *ComponentsBuilder {
	b.subTitle = subTitle
	return b
}

func (b *ComponentsBuilder) WithBody(body string) *ComponentsBuilder {
	b.bodyText = body
	return b
}

func (b *ComponentsBuilder) WithFooter(footer string) *ComponentsBuilder {
	b.footerText = footer
	return b
}

func (b *ComponentsBuilder) WithImage(url string) *ComponentsBuilder {
	b.imageURL = url
	return b
}

func (b *ComponentsBuilder) AddButton(btn discordgo.MessageComponent) *ComponentsBuilder {
	b.buttons = append(b.buttons, btn)
	return b
}

func (b *ComponentsBuilder) WithButtons(btns ...discordgo.MessageComponent) *ComponentsBuilder {
	b.buttons = append(b.buttons, btns...)
	return b
}

func (b *ComponentsBuilder) AddSelectMenu(menu discordgo.MessageComponent) *ComponentsBuilder {
	b.selectMenus = append(b.selectMenus, menu)
	return b
}

func (b *ComponentsBuilder) AddRaw(comp discordgo.MessageComponent) *ComponentsBuilder {
	b.rawComponents = append(b.rawComponents, comp)
	return b
}

func (b *ComponentsBuilder) Build() []discordgo.MessageComponent {
	var comps []discordgo.MessageComponent
	dividerTrue := true
	smallSpacing := discordgo.SeparatorSpacingSizeSmall

	// 1. Title & SubTitle (TextDisplay)
	if b.title != "" {
		if b.subTitle != "" {
			comps = append(comps, discordgo.TextDisplay{
				Content: fmt.Sprintf("### %s\n**%s**", b.title, b.subTitle),
			})
		} else {
			comps = append(comps, discordgo.TextDisplay{
				Content: fmt.Sprintf("### %s", b.title),
			})
		}
	} else if b.subTitle != "" {
		comps = append(comps, discordgo.TextDisplay{
			Content: fmt.Sprintf("**%s**", b.subTitle),
		})
	}

	// 2. Separator if title exists and body/image/raw exists
	if (b.title != "" || b.subTitle != "") && (b.bodyText != "" || b.imageURL != "" || len(b.rawComponents) > 0) {
		comps = append(comps, discordgo.Separator{
			Divider: &dividerTrue,
			Spacing: &smallSpacing,
		})
	}

	// 3. MediaGallery (Top-Level) or Body Text
	if b.imageURL != "" {
		comps = append(comps, discordgo.MediaGallery{
			Items: []discordgo.MediaGalleryItem{
				{
					Media: discordgo.UnfurledMediaItem{
						URL: b.imageURL,
					},
				},
			},
		})
	}
	if b.bodyText != "" {
		comps = append(comps, discordgo.TextDisplay{
			Content: b.bodyText,
		})
	}
	if len(b.rawComponents) > 0 {
		comps = append(comps, b.rawComponents...)
	}

	// 4. Separator if footer exists
	if b.footerText != "" {
		if len(comps) > 0 {
			comps = append(comps, discordgo.Separator{
				Divider: &dividerTrue,
				Spacing: &smallSpacing,
			})
		}
		rawLines := strings.Split(b.footerText, "\n")
		var formattedLines []string
		for _, l := range rawLines {
			trimmed := strings.TrimSpace(l)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "-# ") {
				formattedLines = append(formattedLines, trimmed)
			} else {
				formattedLines = append(formattedLines, fmt.Sprintf("-# %s", trimmed))
			}
		}
		if len(formattedLines) > 0 {
			comps = append(comps, discordgo.TextDisplay{
				Content: strings.Join(formattedLines, "\n"),
			})
		}
	}

	// 5. Select Menus
	for _, menu := range b.selectMenus {
		comps = append(comps, discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{menu},
		})
	}

	// 6. Action Buttons
	if len(b.buttons) > 0 {
		comps = append(comps, discordgo.ActionsRow{
			Components: b.buttons,
		})
	}

	return comps
}

func SimpleComponentsCard(title, body, footer string) []discordgo.MessageComponent {
	return NewComponentsBuilder().
		WithTitle(title).
		WithBody(body).
		WithFooter(footer).
		Build()
}

func SimpleErrorCard(message string) []discordgo.MessageComponent {
	return NewComponentsBuilder().
		WithTitle("알림").
		WithBody(message).
		Build()
}

func RespondComponentsV2(s *discordgo.Session, ic *discordgo.InteractionCreate, comps []discordgo.MessageComponent, ephemeral bool) error {
	var flags discordgo.MessageFlags = discordgo.MessageFlagsIsComponentsV2
	if ephemeral {
		flags |= discordgo.MessageFlagsEphemeral
	}
	return s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Components: comps,
			Flags:      flags,
		},
	})
}

func RespondComponentsV2WithFiles(s *discordgo.Session, ic *discordgo.InteractionCreate, comps []discordgo.MessageComponent, files []*discordgo.File, ephemeral bool) error {
	var flags discordgo.MessageFlags = discordgo.MessageFlagsIsComponentsV2
	if ephemeral {
		flags |= discordgo.MessageFlagsEphemeral
	}
	return s.InteractionRespond(ic.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Components: comps,
			Files:      files,
			Flags:      flags,
		},
	})
}

func EditInteractionComponentsV2(s *discordgo.Session, ic *discordgo.InteractionCreate, comps []discordgo.MessageComponent) (*discordgo.Message, error) {
	if ic == nil || ic.Interaction == nil {
		return nil, fmt.Errorf("nil interaction")
	}
	appID := ic.Interaction.AppID
	if appID == "" && s.State != nil && s.State.User != nil {
		appID = s.State.User.ID
	}
	token := ic.Interaction.Token
	uri := discordgo.EndpointWebhookMessage(appID, token, "@original")

	emptyAttachments := []*discordgo.MessageAttachment{}
	data := struct {
		Components  *[]discordgo.MessageComponent   `json:"components,omitempty"`
		Flags       discordgo.MessageFlags          `json:"flags"`
		Attachments *[]*discordgo.MessageAttachment `json:"attachments,omitempty"`
	}{
		Components:  &comps,
		Flags:       discordgo.MessageFlagsIsComponentsV2,
		Attachments: &emptyAttachments,
	}

	response, err := s.RequestWithBucketID("PATCH", uri, data, discordgo.EndpointWebhookToken("", ""))
	if err != nil {
		return nil, err
	}
	var msg *discordgo.Message
	_ = json.Unmarshal(response, &msg)
	return msg, nil
}
