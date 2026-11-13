package agentic

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
)

func (p *Pipeline) project(ctx context.Context, t *Turn) (*Turn, error) {
	t.Stages = append(t.Stages, RuntimeMessageProjection)
	active := map[string]bool{}
	order := []string{}
	sent := 0
	var send func(Packet) error
	send = func(packet Packet) error {
		if packet.MessageID == "" {
			packet.MessageID = t.AssistantMessageID
		}
		if packet.TurnID == "" {
			packet.TurnID = t.Request.Input.Identity.TurnID
		}
		sent++
		if sent > 256 {
			return errors.New("agentic: projection packet budget exceeded")
		}
		if packet.BizName == "" {
			packet.BizName = "teacher"
		}
		life := t.Contribution.BusinessLifecycle[packet.BizName]
		if packet.Type != "result" && packet.Type != "error" && !active[packet.BizName] {
			if life.BeforeBusiness != nil {
				if e := life.BeforeBusiness(ctx, t); e != nil {
					return e
				}
			}
			active[packet.BizName] = true
			order = append(order, packet.BizName)
		}
		for _, h := range t.Contribution.PacketHooks {
			if h.BeforeSend != nil {
				allow, e := h.BeforeSend(ctx, &packet)
				if e != nil {
					return e
				}
				if !allow {
					return nil
				}
			}
		}
		if e := t.Sink.Send(ctx, packet); e != nil {
			return e
		}
		for _, h := range t.Contribution.PacketHooks {
			if h.AfterSend != nil {
				if e := h.AfterSend(ctx, packet); e != nil {
					return e
				}
			}
		}
		if packet.EndBusiness && active[packet.BizName] {
			active[packet.BizName] = false
			if life.AfterBusiness != nil {
				return life.AfterBusiness(ctx, t)
			}
		}
		return nil
	}
	t.Emit = func(c context.Context, packet Packet) error {
		if e := c.Err(); e != nil {
			return e
		}
		return send(packet)
	}
	defer func() { t.Emit = nil }()
	if t.ShortCircuit {
		for _, packet := range t.Packets {
			if e := send(packet); e != nil {
				return t, e
			}
		}
	} else {
		defer t.Source.Close()
		for {
			ev, ok := t.Source.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case "message":
				m := ev.Message
				if m == nil {
					continue
				}
				if m.Role == schema.Assistant && len(m.ToolCalls) == 0 && m.Content != "" {
					if e := send(Packet{Type: "text", Text: m.Content, EndBusiness: true}); e != nil {
						return t, e
					}
				} else if m.Role == schema.Tool {
					if handler := t.Contribution.ToolHandlers[m.ToolName]; handler != nil {
						packets, e := handler(ctx, m.Content)
						if e != nil {
							return t, e
						}
						for _, packet := range packets {
							if e = send(packet); e != nil {
								return t, e
							}
						}
					}
				}
			case "turn_result":
				if ev.Result != nil {
					t.Result = *ev.Result
					if e := send(Packet{Type: "result", Status: t.Result.Status, CompletionID: t.Result.CompletionID, ErrorCode: t.Result.ErrorCode, WarningCode: t.Result.WarningCode}); e != nil {
						return t, e
					}
				}
			case "error":
				if e := send(Packet{Type: "error", ErrorCode: ev.ErrorCode}); e != nil {
					return t, e
				}
			}
		}
		if e := ctx.Err(); e != nil {
			return t, e
		}
		t.Result, t.Err = t.Source.Result(ctx)
	}
	// Called only after EOF: the main turn has finalized its Agent Store writes.
	for _, name := range order {
		if !active[name] {
			continue
		}
		active[name] = false
		if hook := t.Contribution.BusinessLifecycle[name].AfterBusiness; hook != nil {
			if e := hook(ctx, t); e != nil {
				return t, e
			}
		}
	}
	for _, hook := range t.Contribution.PostRuntimeHooks {
		if e := hook(ctx, t); e != nil {
			return t, e
		}
	}
	return t, nil
}
