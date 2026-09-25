package main

import (
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func getInteractionOptionString(ic *discordgo.InteractionCreate, name string) string {
	for _, option := range ic.ApplicationCommandData().Options {
		if option.Name == name && option.Value != nil {
			return fmt.Sprint(option.Value)
		}
	}
	return ""
}

func getInteractionOptionBoolWithDefault(ic *discordgo.InteractionCreate, name string, defaultValue bool) bool {
	for _, option := range ic.ApplicationCommandData().Options {
		if option.Name == name && option.Value != nil {
			if b, ok := option.Value.(bool); ok {
				return b
			}
			if s, ok := option.Value.(string); ok {
				return s == "true"
			}
		}
	}
	return defaultValue
}

func getInteractionOptionBool(ic *discordgo.InteractionCreate, name string) (bool, bool) {
	for _, option := range ic.ApplicationCommandData().Options {
		if option.Name == name && option.Value != nil {
			if b, ok := option.Value.(bool); ok {
				return b, true
			}
			if s, ok := option.Value.(string); ok {
				return s == "true", true
			}
		}
	}
	return false, false
}

func getUserID(ic *discordgo.InteractionCreate) string {
	if ic.User != nil {
		return ic.User.ID
	}
	if ic.Member != nil && ic.Member.User != nil {
		return ic.Member.User.ID
	}
	return ""
}

// isInteractionOwnerOrAdmin checks if the interaction caller is the owner of the session,
// or is a whitelisted bot user.
func isInteractionOwnerOrAdmin(ic *discordgo.InteractionCreate, ownerID string) bool {
	if ic == nil {
		return false
	}
	userID := getUserID(ic)
	if userID == "" {
		return false
	}
	// 1. Exact match with session owner
	if ownerID != "" && userID == ownerID {
		return true
	}
	// 2. Whitelisted bot users
	if isSlashCommandAllowed(userID) {
		return true
	}
	return false
}

func messageMentionsBot(message *discordgo.MessageCreate, botID string) bool {
	if botID == "" {
		return false
	}
	for _, mention := range message.Mentions {
		if mention.ID == botID {
			return true
		}
	}
	return strings.Contains(message.Content, "<@"+botID+">") || strings.Contains(message.Content, "<@!"+botID+">")
}

func stripBotMention(content, botID string) string {
	content = strings.ReplaceAll(content, "<@"+botID+">", "")
	content = strings.ReplaceAll(content, "<@!"+botID+">", "")
	return strings.TrimSpace(content)
}

func getUserName(ic *discordgo.InteractionCreate) string {
	if ic.Member != nil && ic.Member.User != nil {
		if ic.Member.Nick != "" {
			return ic.Member.Nick
		}
		if ic.Member.User.GlobalName != "" {
			return ic.Member.User.GlobalName
		}
		return ic.Member.User.Username
	}
	if ic.User != nil {
		if ic.User.GlobalName != "" {
			return ic.User.GlobalName
		}
		return ic.User.Username
	}
	return "사용자"
}

func getInteractionOptionUser(s *discordgo.Session, ic *discordgo.InteractionCreate, name string) *discordgo.User {
	for _, option := range ic.ApplicationCommandData().Options {
		if option.Name == name && option.Value != nil {
			return option.UserValue(s)
		}
	}
	return nil
}
