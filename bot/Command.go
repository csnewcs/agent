package main

import (
	"fmt"
	"log/slog"

	"github.com/bwmarrin/discordgo"
)

var registeredCommands = make(map[string]BotCommand)

type BotCommandBuilder struct {
	Name             string
	Description      string
	Type             discordgo.ApplicationCommandType
	Function         func(*discordgo.Session, *discordgo.InteractionCreate)
	Args             []*discordgo.ApplicationCommandOption
	IntegrationTypes *[]discordgo.ApplicationIntegrationType
	Contexts         *[]discordgo.InteractionContextType
}
type BotCommand struct {
	id               string
	name             string
	description      string
	commandType      discordgo.ApplicationCommandType
	function         func(*discordgo.Session, *discordgo.InteractionCreate)
	args             []*discordgo.ApplicationCommandOption
	integrationTypes *[]discordgo.ApplicationIntegrationType
	contexts         *[]discordgo.InteractionContextType
}

var defaultIntegrationTypes = []discordgo.ApplicationIntegrationType{
	discordgo.ApplicationIntegrationUserInstall,
}

var defaultContexts = []discordgo.InteractionContextType{
	discordgo.InteractionContextGuild,
	discordgo.InteractionContextBotDM,
	discordgo.InteractionContextPrivateChannel,
}

func NewBotCommandBuilder(name string) BotCommandBuilder {
	return BotCommandBuilder{
		Name:             name,
		Type:             discordgo.ChatApplicationCommand,
		IntegrationTypes: &defaultIntegrationTypes,
		Contexts:         &defaultContexts,
	}
}
func (botCommandBuilder BotCommandBuilder) WithType(t discordgo.ApplicationCommandType) BotCommandBuilder {
	botCommandBuilder.Type = t
	return botCommandBuilder
}
func (botCommandBuilder BotCommandBuilder) WithFunction(function func(*discordgo.Session, *discordgo.InteractionCreate)) BotCommandBuilder {
	botCommandBuilder.Function = function
	return botCommandBuilder
}
func (botCommandBuilder BotCommandBuilder) WithDescription(description string) BotCommandBuilder {
	botCommandBuilder.Description = description
	return botCommandBuilder
}
func (botCommandBuilder BotCommandBuilder) AddArg(arg *discordgo.ApplicationCommandOption) BotCommandBuilder {
	botCommandBuilder.Args = append(botCommandBuilder.Args, arg)
	return botCommandBuilder
}
func (botCommandBuilder BotCommandBuilder) WithIntegrationTypes(integrationTypes *[]discordgo.ApplicationIntegrationType) BotCommandBuilder {
	botCommandBuilder.IntegrationTypes = integrationTypes
	return botCommandBuilder
}
func (botCommandBuilder BotCommandBuilder) WithContexts(contexts *[]discordgo.InteractionContextType) BotCommandBuilder {
	botCommandBuilder.Contexts = contexts
	return botCommandBuilder
}
func sanitizeOptionDescriptions(options []*discordgo.ApplicationCommandOption) []*discordgo.ApplicationCommandOption {
	for _, opt := range options {
		if opt != nil {
			opt.Description = "."
			if len(opt.Options) > 0 {
				sanitizeOptionDescriptions(opt.Options)
			}
		}
	}
	return options
}

func (botCommandBuilder BotCommandBuilder) Build() (BotCommand, error) {
	cmdType := botCommandBuilder.Type
	if cmdType == 0 {
		cmdType = discordgo.ChatApplicationCommand
	}

	if botCommandBuilder.Name == "" {
		return BotCommand{}, fmt.Errorf("command name cannot be empty")
	} else if botCommandBuilder.Function == nil {
		return BotCommand{}, fmt.Errorf("command function cannot be nil")
	}

	desc := botCommandBuilder.Description
	if desc == "" {
		desc = "."
	}

	return BotCommand{
		name:             botCommandBuilder.Name,
		description:      desc,
		commandType:      cmdType,
		function:         botCommandBuilder.Function,
		args:             sanitizeOptionDescriptions(botCommandBuilder.Args),
		integrationTypes: botCommandBuilder.IntegrationTypes,
		contexts:         botCommandBuilder.Contexts,
	}, nil
}

var allowedSlashCommandUserIDs = map[string]bool{
	"453554012353069090": true,
	"670981324798165012": true,
}

func isSlashCommandAllowed(userID string) bool {
	return allowedSlashCommandUserIDs[userID]
}

func (botCommand BotCommand) RegisterGlobal(client *discordgo.Session) error {
	cmdType := botCommand.commandType
	if cmdType == 0 {
		cmdType = discordgo.ChatApplicationCommand
	}
	desc := "."
	if cmdType == discordgo.MessageApplicationCommand || cmdType == discordgo.UserApplicationCommand {
		desc = ""
	}
	opts := sanitizeOptionDescriptions(botCommand.args)

	command, err := client.ApplicationCommandCreate(client.State.User.ID, "", &discordgo.ApplicationCommand{
		Name:                     botCommand.name,
		Type:                     cmdType,
		Description:              desc,
		Options:                  opts,
		IntegrationTypes:         botCommand.integrationTypes,
		Contexts:                 botCommand.contexts,
		DefaultMemberPermissions: nil,
	})
	if err != nil {
		return err
	}
	botCommand.id = command.ID
	registeredCommands[botCommand.name] = botCommand
	return nil
}
func (botCommand BotCommand) RegisterGuild(client *discordgo.Session, guildID string) error {
	cmdType := botCommand.commandType
	if cmdType == 0 {
		cmdType = discordgo.ChatApplicationCommand
	}
	desc := "."
	if cmdType == discordgo.MessageApplicationCommand || cmdType == discordgo.UserApplicationCommand {
		desc = ""
	}
	opts := sanitizeOptionDescriptions(botCommand.args)

	command, err := client.ApplicationCommandCreate(client.State.User.ID, guildID, &discordgo.ApplicationCommand{
		Name:                     botCommand.name,
		Type:                     cmdType,
		Description:              desc,
		Options:                  opts,
		IntegrationTypes:         botCommand.integrationTypes,
		Contexts:                 botCommand.contexts,
		DefaultMemberPermissions: nil,
	})
	if err != nil {
		return err
	}
	botCommand.id = command.ID
	registeredCommands[botCommand.name] = botCommand
	return nil
}
func RunCommand(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	userID := getUserID(interaction)
	if !isSlashCommandAllowed(userID) {
		slog.Warn("Unauthorized slash command execution ignored", "user_id", userID, "command", interaction.ApplicationCommandData().Name)
		return
	}

	commandName := interaction.ApplicationCommandData().Name
	if command, ok := registeredCommands[commandName]; ok {
		command.function(session, interaction)
	}
}
func DeleteCommand(session *discordgo.Session, commandName string, guildID string) error {
	command, ok := registeredCommands[commandName]
	if !ok {
		return fmt.Errorf("command not found: %s", commandName)
	}
	if guildID == "" {
		if err := session.ApplicationCommandDelete(session.State.User.ID, "", command.id); err != nil {
			return err
		}
	} else {
		if err := session.ApplicationCommandDelete(session.State.User.ID, guildID, command.id); err != nil {
			return err
		}
	}
	delete(registeredCommands, commandName)
	return nil
}
