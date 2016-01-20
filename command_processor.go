package gohome

import (
	"fmt"

	"github.com/markdaws/gohome/cmd"
	"github.com/markdaws/gohome/log"
)

type CommandProcessor interface {
	Process()
	Enqueue(cmd.Command) error
	SetSystem(s *System)
}

func NewCommandProcessor() CommandProcessor {
	return &commandProcessor{
		commands: make(chan *cmd.Func, 10000),
	}
}

type commandProcessor struct {
	commands chan *cmd.Func
	system   *System
}

func (cp *commandProcessor) SetSystem(s *System) {
	cp.system = s
}

func (cp *commandProcessor) Process() {
	//TODO: Have multiple workers?
	for c := range cp.commands {
		err := c.Func()
		if err != nil {
			log.W("cmpProcesor:execute error:%s", err)
		} else {
			log.V("cmdProcessor:executed:%s", c)
		}
	}
}

func (cp *commandProcessor) Enqueue(c cmd.Command) error {
	log.V("cmdProcessor:enqueue:%s", c)

	switch command := c.(type) {
	case *cmd.ZoneSetLevel:
		z, ok := cp.system.Zones[command.ZoneGlobalID]
		if !ok {
			return fmt.Errorf("unknown zone ID %s", command.ZoneGlobalID)
		}
		zCmd, err := z.Device.BuildCommand(command)
		if err != nil {
			return err
		}
		cp.commands <- zCmd

	case *cmd.SceneSet:
		s, ok := cp.system.Scenes[command.SceneGlobalID]
		if !ok {
			return fmt.Errorf("unknown scene ID %s", command.SceneGlobalID)
		}
		for _, sceneCmd := range s.Commands {
			err := cp.Enqueue(sceneCmd)
			if err != nil {
				return err
			}
		}

	case *cmd.ButtonPress:
		b, ok := cp.system.Buttons[command.ButtonGlobalID]
		if !ok {
			return fmt.Errorf("unknown button ID %s", command.ButtonGlobalID)
		}
		bCmd, err := b.Device.BuildCommand(command)
		if err != nil {
			return err
		}
		cp.commands <- bCmd

	case *cmd.ButtonRelease:
		b, ok := cp.system.Buttons[command.ButtonGlobalID]
		if !ok {
			return fmt.Errorf("unknown button ID %s", command.ButtonGlobalID)
		}
		bCmd, err := b.Device.BuildCommand(command)
		if err != nil {
			return err
		}
		cp.commands <- bCmd

	default:
		return fmt.Errorf("unknown command, cannot process")
	}
	return nil
}