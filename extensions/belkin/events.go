package belkin

import (
	"html"
	"regexp"
	"strconv"
	"time"

	belkinExt "github.com/go-home-iot/belkin"
	"github.com/go-home-iot/event-bus"
	"github.com/go-home-iot/upnp"
	"github.com/markdaws/gohome"
	"github.com/markdaws/gohome/cmd"
	"github.com/markdaws/gohome/log"
)

type consumer struct {
	System     *gohome.System
	Device     *gohome.Device
	Name       string
	DeviceType belkinExt.DeviceType
}

func (c *consumer) ConsumerName() string {
	return c.Name
}
func (c *consumer) StartConsuming(ch chan evtbus.Event) {
	go func() {
		for e := range ch {
			switch evt := e.(type) {
			case *gohome.ZonesReportEvt:
				for _, zone := range c.Device.OwnedZones(evt.ZoneIDs) {
					dev := &belkinExt.Device{
						Scan: belkinExt.ScanResponse{
							SearchType: string(c.DeviceType),
							Location:   c.Device.Address,
						},
					}

					switch c.DeviceType {
					case belkinExt.DTMaker:
						attrs, err := dev.FetchAttributes(time.Second * 5)
						if err != nil {
							log.V("Belkin - failed to fetch attrs: %s", err)
							continue
						}

						if attrs.Switch != nil {
							c.System.Services.EvtBus.Enqueue(&gohome.ZoneLevelChangedEvt{
								ZoneName: zone.Name,
								ZoneID:   zone.ID,
								Level:    cmd.Level{Value: float32(*attrs.Switch)},
							})
						}

					case belkinExt.DTInsight:
						state, err := dev.FetchBinaryState(time.Second * 5)
						if err != nil {
							log.V("Belkin - failed to fetch binary state: %s", err)
							continue
						}

						c.System.Services.EvtBus.Enqueue(&gohome.ZoneLevelChangedEvt{
							ZoneName: zone.Name,
							ZoneID:   zone.ID,
							Level:    cmd.Level{Value: float32(state)},
						})
					}
				}

			case *gohome.SensorsReportEvt:
				for _, sensor := range c.Device.OwnedSensors(evt.SensorIDs) {
					dev := &belkinExt.Device{
						Scan: belkinExt.ScanResponse{
							SearchType: string(c.DeviceType),
							Location:   c.Device.Address,
						},
					}
					attrs, err := dev.FetchAttributes(time.Second * 5)
					if err != nil {
						log.V("Belkin - failed to fetch attrs: %s", err)
						continue
					}

					if attrs.Sensor != nil {
						attr := sensor.Attr
						attr.Value = strconv.Itoa(*attrs.Sensor)
						c.System.Services.EvtBus.Enqueue(&gohome.SensorAttrChangedEvt{
							SensorName: sensor.Name,
							SensorID:   sensor.ID,
							Attr:       attr,
						})
					}

				}
			}
		}
	}()
}
func (c *consumer) StopConsuming() {
	//TODO:
}

type producer struct {
	System     *gohome.System
	Device     *gohome.Device
	Name       string
	SID        string
	Producing  bool
	DeviceType belkinExt.DeviceType
}

var attrRegexp = regexp.MustCompile(`(<attributeList>.*</attributeList>)`)
var binaryRegexp = regexp.MustCompile(`(<BinaryState>.*</BinaryState>)`)

//==================== upnp.Subscriber interface ========================

func (p *producer) UPNPNotify(e upnp.NotifyEvent) {
	if !p.Producing {
		return
	}

	// Contents are double HTML encoded when returned from the device
	body := html.UnescapeString(html.UnescapeString(e.Body))

	// We only have one zone and sensor, have to check incase we don't currently have
	// a zone or sensor imported
	zone, hasZone := p.Device.Zones["1"]
	sensor, hasSensor := p.Device.Sensors["1"]

	// This could be a response with an attribute list, or it could be a binary state property
	attrList := attrRegexp.FindStringSubmatch(body)
	if attrList != nil && len(attrList) != 0 {
		attrs := belkinExt.ParseAttributeList(attrList[0])
		if attrs == nil {
			return
		}

		if attrs.Sensor != nil && hasSensor {
			p.System.Services.EvtBus.Enqueue(&gohome.SensorAttrChangedEvt{
				SensorID:   sensor.ID,
				SensorName: sensor.Name,
				Attr: gohome.SensorAttr{
					Name:     "sensor",
					Value:    strconv.Itoa(*attrs.Sensor),
					DataType: gohome.SDTInt,
					States:   sensor.Attr.States,
				},
			})
		} else if attrs.Switch != nil && hasZone {
			p.System.Services.EvtBus.Enqueue(&gohome.ZoneLevelChangedEvt{
				ZoneName: zone.Name,
				ZoneID:   zone.ID,
				Level:    cmd.Level{Value: float32(*attrs.Switch)},
			})
		}
	} else if hasZone {
		binary := binaryRegexp.FindStringSubmatch(body)
		if binary == nil || len(binary) == 0 {
			return
		}

		states := belkinExt.ParseBinaryState(binary[0])

		// Note for onoff 1 and 8 mean on, normalize to 1
		level := states.OnOff
		if level == 8 {
			level = 1
		}
		p.System.Services.EvtBus.Enqueue(&gohome.ZoneLevelChangedEvt{
			ZoneName: zone.Name,
			ZoneID:   zone.ID,
			Level:    cmd.Level{Value: float32(level)},
		})
	}
}

//=======================================================================

func (p *producer) ProducerName() string {
	return p.Name
}

func (p *producer) StartProducing(b *evtbus.Bus) {
	log.V("producer [%s] start producing", p.ProducerName())

	go func() {
		p.Producing = true

		for p.Producing {
			log.V("%s - subscribing to UPNP, SID:%s", p.ProducerName(), p.SID)

			// The make has a sensor and a switch state, need to notify these changes
			// to the event bus
			sid, err := p.System.Services.UPNP.Subscribe(
				p.Device.Address+"/upnp/event/basicevent1",
				p.SID,
				120,
				true,
				p)

			if err != nil {
				// log failure, keep trying to subscribe to the target device
				// there may be network issues, if this is a renew, the old SID
				// might have expired, so reset so we get a new one
				log.V("[%s] failed to subscribe to upnp: %s", p.ProducerName(), err)
				p.SID = ""
				time.Sleep(time.Second * 10)
			} else {
				// We got a sid, now sleep then renew the subscription
				p.SID = sid
				log.V("%s - subscribed to UPNP, SID:%s", p.ProducerName(), sid)
				time.Sleep(time.Second * 100)
			}
		}

		log.V("%s - stopped producing events", p.ProducerName())
	}()
}

func (p *producer) StopProducing() {
	p.Producing = false

	err := p.System.Services.UPNP.Unsubscribe(p.SID)
	if err != nil {
		log.V("error during unsusbscribe [%s]: %s", p.ProducerName(), err)
	}
}