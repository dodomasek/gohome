package gohome

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/markdaws/gohome/cmd"
	"github.com/markdaws/gohome/comm"
	"github.com/markdaws/gohome/log"
)

type Lbdgpro2whDevice struct {
	device
}

func (d *Lbdgpro2whDevice) ModelNumber() string {
	return "L-BDGPRO2-WH"
}

func (d *Lbdgpro2whDevice) InitConnections() {
	ci := *d.connectionInfo.(*comm.TelnetConnectionInfo)
	createConnection := func() comm.Connection {
		conn := comm.NewTelnetConnection(ci)
		conn.SetPingCallback(func() error {
			if _, err := conn.Write([]byte("#PING\r\n")); err != nil {
				return fmt.Errorf("%s ping failed: %s", d, err)
			}
			return nil
		})
		return conn
	}
	ps := ci.PoolSize
	log.V("%s init connections, pool size %d", d, ps)
	d.pool = comm.NewConnectionPool(d.name, ps, createConnection)
	log.V("%s connected", d)
}

func (d *Lbdgpro2whDevice) StartProducingEvents() (<-chan Event, <-chan bool) {
	d.evpDone = make(chan bool)
	d.evpFire = make(chan Event)

	if d.Stream() {
		go startStreaming(d)
	}
	return d.evpFire, d.evpDone
}

func (d *Lbdgpro2whDevice) Authenticate(c comm.Connection) error {
	r := bufio.NewReader(c)
	_, err := r.ReadString(':')
	if err != nil {
		return fmt.Errorf("authenticate login failed: %s", err)
	}

	info := c.Info().(comm.TelnetConnectionInfo)
	_, err = c.Write([]byte(info.Login + "\r\n"))
	if err != nil {
		return fmt.Errorf("authenticate write login failed: %s", err)
	}

	_, err = r.ReadString(':')
	if err != nil {
		return fmt.Errorf("authenticate password failed: %s", err)
	}

	_, err = c.Write([]byte(info.Password + "\r\n"))
	if err != nil {
		return fmt.Errorf("authenticate password failed: %s", err)
	}
	return nil
}

func (d *Lbdgpro2whDevice) BuildCommand(c cmd.Command) (*cmd.Func, error) {
	switch command := c.(type) {
	case *cmd.ZoneSetLevel:
		return &cmd.Func{
			Func: func() error {
				newCmd := &StringCommand{
					Device: d,
					Value:  "#OUTPUT," + command.ZoneLocalID + ",1,%.2f\r\n",
					Args:   []interface{}{command.Level},
				}
				return newCmd.Execute()
			},
		}, nil
	case *cmd.ButtonPress:
		return &cmd.Func{
			Func: func() error {
				newCmd := &StringCommand{
					Device: d,
					Value:  "#DEVICE," + command.DeviceLocalID + "," + command.ButtonLocalID + ",3\r\n",
				}
				return newCmd.Execute()
			},
		}, nil

	case *cmd.ButtonRelease:
		return &cmd.Func{
			Func: func() error {
				cmd := &StringCommand{
					Device: d,
					Value:  "#DEVICE," + command.DeviceLocalID + "," + command.ButtonLocalID + ",4\r\n",
				}
				return cmd.Execute()
			},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported command type")
	}
}

func startStreaming(d *Lbdgpro2whDevice) {
	//TODO: Stop?
	for {
		err := stream(d)
		if err != nil {
			log.E("%s streaming failed: %s", d, err)
		}
		time.Sleep(10 * time.Second)
	}
}

func stream(d *Lbdgpro2whDevice) error {
	log.V("%s attemping to stream events", d)
	conn, err := d.Connect()
	if err != nil {
		return fmt.Errorf("%s unable to connect to stream events: %s", d, err)
	}

	defer func() {
		d.ReleaseConnection(conn)
	}()

	log.V("%s streaming events", d)
	scanner := bufio.NewScanner(conn)
	split := func(data []byte, atEOF bool) (advance int, token []byte, err error) {

		//Match first instance of ~OUTPUT|~DEVICE.*\r\n
		str := string(data[0:])
		indices := regexp.MustCompile("[~|#][OUTPUT|DEVICE].+\r\n").FindStringIndex(str)

		//TODO: Don't let input grow forever - remove beginning chars after reaching max length

		if indices != nil {
			token = []byte(string([]rune(str)[indices[0]:indices[1]]))
			advance = indices[1]
			err = nil
		} else {
			advance = 0
			token = nil
			err = nil
		}
		return
	}

	scanner.Split(split)
	for scanner.Scan() {
		if d.evpFire != nil {
			orig := scanner.Text()
			if cmd := parseCommandString(d, orig); cmd != nil {
				d.evpFire <- NewEvent(d, cmd, orig, ETUnknown)
			}
		}
	}

	log.V("%s stopped streaming events", d)
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s error streaming events, streaming stopped: %s", d, err)
	}
	return nil

	/*
		//TODO: When?
		if d.evpDone != nil {
			close(d.evpDone)
		}
	*/
}

//TODO: put on device
func parseCommandString(d *Lbdgpro2whDevice, cmd string) cmd.Command {
	switch {
	case strings.HasPrefix(cmd, "~OUTPUT"),
		strings.HasPrefix(cmd, "#OUTPUT"):
		return parseZoneCommand(d, cmd)

	case strings.HasPrefix(cmd, "~DEVICE"),
		strings.HasPrefix(cmd, "#DEVICE"):
		return parseDeviceCommand(d, cmd)
	default:
		// Ignore commands we don't care about
		return nil
	}
}

//TODO: Put on device
func parseDeviceCommand(d *Lbdgpro2whDevice, command string) cmd.Command {
	matches := regexp.MustCompile("[~|#]DEVICE,([^,]+),([^,]+),(.+)\r\n").FindStringSubmatch(command)
	if matches == nil || len(matches) != 4 {
		return nil
	}

	deviceID := matches[1]
	componentID := matches[2]
	cmdID := matches[3]
	sourceDevice := d.Devices()[deviceID]
	if sourceDevice == nil {
		return nil
	}

	var finalCmd cmd.Command
	switch cmdID {
	case "3":
		if btn := sourceDevice.Buttons()[componentID]; btn != nil {
			finalCmd = &cmd.ButtonPress{
				ButtonLocalID:  btn.LocalID,
				ButtonGlobalID: btn.GlobalID,
				DeviceName:     d.Name(),
				DeviceLocalID:  d.LocalID(),
				DeviceGlobalID: d.GlobalID(),
			}
		}
	case "4":
		if btn := sourceDevice.Buttons()[componentID]; btn != nil {
			finalCmd = &cmd.ButtonRelease{
				ButtonLocalID:  btn.LocalID,
				ButtonGlobalID: btn.GlobalID,
				DeviceName:     d.Name(),
				DeviceLocalID:  d.LocalID(),
				DeviceGlobalID: d.GlobalID(),
			}
		}
	default:
		return nil
	}

	return finalCmd
}

//TODO: put on device
func parseZoneCommand(d *Lbdgpro2whDevice, command string) cmd.Command {
	matches := regexp.MustCompile("[~|?]OUTPUT,([^,]+),([^,]+),(.+)\r\n").FindStringSubmatch(command)
	if matches == nil || len(matches) != 4 {
		return nil
	}

	zoneID := matches[1]
	cmdID := matches[2]
	level, err := strconv.ParseFloat(matches[3], 64)
	if err != nil {
		return nil
	}

	z := d.Zones()[zoneID]
	if z == nil {
		return nil
	}

	var finalCmd cmd.Command
	switch cmdID {
	case "1":
		finalCmd = &cmd.ZoneSetLevel{
			ZoneLocalID:  z.LocalID,
			ZoneGlobalID: z.GlobalID,
			ZoneName:     z.Name,
			Level:        float32(level),
		}
	default:
		return nil
	}

	return finalCmd
}