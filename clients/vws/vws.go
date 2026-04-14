package vws

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-vws/client"
	cfg "github.com/tsarna/vinculum/config"
)

func init() {
	cfg.RegisterClientType("vws", process)
}

type VinculumWebsocketClient struct {
	cfg.BaseBusClient
	ClientBuilder *client.ClientBuilder
}

func (c *VinculumWebsocketClient) Build() (bus.Client, error) {
	if c.Subscriber != nil {
		c.ClientBuilder = c.ClientBuilder.WithSubscriber(c.Subscriber)
	} else {
		c.ClientBuilder = c.ClientBuilder.WithSubscriber(&bus.BaseSubscriber{})
	}

	busClient, err := c.ClientBuilder.Build()
	if err != nil {
		return nil, err
	}
	c.Client = busClient

	return busClient, nil
}

type VinculumWebsocketsClientDefinition struct {
	Url            string                   `hcl:"url"`
	DialTimeout    hcl.Expression           `hcl:"dial_timeout,optional"`
	WriteQueueSize *int                     `hcl:"write_queue_size,optional"`
	AuthExpression hcl.Expression           `hcl:"auth,optional"`
	Headers        map[string]string        `hcl:"headers,optional"`
	Reconnect      *cfg.ReconnectDefinition `hcl:"reconnect,optional"`
	DefRange       hcl.Range                `hcl:",def_range"`
}

func process(config *cfg.Config, block *hcl.Block, remainingBody hcl.Body) (cfg.Client, hcl.Diagnostics) {
	clientDef := VinculumWebsocketsClientDefinition{}
	diags := gohcl.DecodeBody(remainingBody, config.EvalCtx(), &clientDef)
	if diags.HasErrors() {
		return nil, diags
	}

	vinculumWebsocketsClients, ok := config.Clients["vws"]
	if !ok {
		vinculumWebsocketsClients = make(map[string]cfg.Client)
		config.Clients["vws"] = vinculumWebsocketsClients
	}

	existing, ok := vinculumWebsocketsClients[block.Labels[1]]
	if ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Vinculum WebSockets client already defined",
				Detail:   fmt.Sprintf("Vinculum WebSockets client %s already defined at %s", block.Labels[1], existing.GetDefRange()),
				Subject:  &clientDef.DefRange,
			},
		}
	}

	clientBuilder := client.NewClient().
		WithLogger(config.Logger).
		WithURL(clientDef.Url)

	if cfg.IsExpressionProvided(clientDef.DialTimeout) {
		dialTimeout, diags := config.ParseDuration(clientDef.DialTimeout)
		if diags.HasErrors() {
			return nil, diags
		}
		clientBuilder = clientBuilder.WithDialTimeout(dialTimeout)
	}

	if clientDef.WriteQueueSize != nil {
		clientBuilder = clientBuilder.WithWriteChannelSize(*clientDef.WriteQueueSize)
	}

	if clientDef.Headers != nil {
		for key, value := range clientDef.Headers {
			clientBuilder = clientBuilder.WithHeader(key, value)
		}
	}

	if cfg.IsExpressionProvided(clientDef.AuthExpression) {
		// @@@ TODO
	}

	if clientDef.Reconnect != nil {
		reconnector, diags := config.CreateReconnector(*clientDef.Reconnect)
		if diags.HasErrors() {
			return nil, diags
		}
		clientBuilder = clientBuilder.WithMonitor(reconnector)
	}

	c := VinculumWebsocketClient{
		BaseBusClient: cfg.BaseBusClient{
			BaseClient: cfg.BaseClient{
				Name:     block.Labels[1],
				DefRange: clientDef.DefRange,
			},
		},
		ClientBuilder: clientBuilder,
	}

	return &c, nil
}
