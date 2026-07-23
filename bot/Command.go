package main

import (
	"fmt"

	"github.com/bwmarrin/discordgo"
)

var registeredCommands = make(map[string]BotCommand)

type BotCommandBuilder struct {
	Name             string
	Description      string
	Function         func(*discordgo.Session, *discordgo.InteractionCreate)
	Args             []*discordgo.ApplicationCommandOption
	IntegrationTypes *[]discordgo.ApplicationIntegrationType
	Contexts         *[]discordgo.InteractionContextType
}
type BotCommand struct {
	id               string
	name             string
	description      string
	function         func(*discordgo.Session, *discordgo.InteractionCreate)
	args             []*discordgo.ApplicationCommandOption
	integrationTypes *[]discordgo.ApplicationIntegrationType
	contexts         *[]discordgo.InteractionContextType
}

func NewBotCommandBuilder(name string) BotCommandBuilder {
	return BotCommandBuilder{Name: name}
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
func (botCommandBuilder BotCommandBuilder) Build() (BotCommand, error) {
	if botCommandBuilder.Name == "" {
		return BotCommand{}, fmt.Errorf("command name cannot be empty")
	} else if botCommandBuilder.Description == "" {
		return BotCommand{}, fmt.Errorf("command description cannot be empty")
	} else if botCommandBuilder.Function == nil {
		return BotCommand{}, fmt.Errorf("command function cannot be nil")
	}

	return BotCommand{
		name:             botCommandBuilder.Name,
		description:      botCommandBuilder.Description,
		function:         botCommandBuilder.Function,
		args:             botCommandBuilder.Args,
		integrationTypes: botCommandBuilder.IntegrationTypes,
		contexts:         botCommandBuilder.Contexts,
	}, nil
}
func (botCommand BotCommand) RegisterGlobal(client *discordgo.Session) error {
	command, err := client.ApplicationCommandCreate(client.State.User.ID, "", &discordgo.ApplicationCommand{
		Name:             botCommand.name,
		Description:      botCommand.description,
		Options:          botCommand.args,
		IntegrationTypes: botCommand.integrationTypes,
		Contexts:         botCommand.contexts,
	})
	if err != nil {
		return err
	}
	botCommand.id = command.ID
	registeredCommands[botCommand.name] = botCommand
	return nil
}
func (botCommand BotCommand) RegisterGuild(client *discordgo.Session, guildID string) error {
	command, err := client.ApplicationCommandCreate(client.State.User.ID, guildID, &discordgo.ApplicationCommand{
		Name:             botCommand.name,
		Description:      botCommand.description,
		Options:          botCommand.args,
		IntegrationTypes: botCommand.integrationTypes,
		Contexts:         botCommand.contexts,
	})
	if err != nil {
		return err
	}
	botCommand.id = command.ID
	registeredCommands[botCommand.name] = botCommand
	return nil
}
func RunCommand(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
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
