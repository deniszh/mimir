// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/alertmanager/distributor.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package alertmanager

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-kit/log/level"
	"github.com/grafana/alerting/definition"

	"github.com/grafana/mimir/pkg/alertmanager/alertspb"
)

// createUsableGrafanaConfig creates an amConfig from a GrafanaAlertConfigDesc.
// If provided, it assigns the global section from the Mimir config to the Grafana config.
// The SMTP and HTTP settings in this section can be used to configure Grafana receivers.
func (am *MultitenantAlertmanager) createUsableGrafanaConfig(gCfg alertspb.GrafanaAlertConfigDesc, rawMimirConfig string) (amConfig, error) {
	externalURL, err := url.Parse(gCfg.ExternalUrl)
	if err != nil {
		return amConfig{}, err
	}

	var amCfg GrafanaAlertmanagerConfig
	if err := json.Unmarshal([]byte(gCfg.RawConfig), &amCfg); err != nil {
		return amConfig{}, fmt.Errorf("failed to unmarshal Grafana Alertmanager configuration: %w", err)
	}

	if rawMimirConfig != "" {
		cfg, err := definition.LoadCompat([]byte(rawMimirConfig))
		if err != nil {
			return amConfig{}, fmt.Errorf("failed to unmarshal Mimir Alertmanager configuration: %w", err)
		}
		amCfg.AlertmanagerConfig.Global = cfg.Global
	}

	// Check for duplicate receiver names.
	nameToReceiver := make(map[string]*definition.PostableApiReceiver, len(amCfg.AlertmanagerConfig.Receivers))
	for _, receiver := range amCfg.AlertmanagerConfig.Receivers {
		if existing, ok := nameToReceiver[receiver.Name]; ok {
			itypes := make([]string, 0, len(existing.GrafanaManagedReceivers))
			for _, i := range existing.GrafanaManagedReceivers {
				itypes = append(itypes, i.Type)
			}
			level.Debug(am.logger).Log("msg", "receiver with same name is defined multiple times. Only the last one will be used", "receiver_name", receiver.Name, "overwritten_integrations", strings.Join(itypes, ","))
		}
		nameToReceiver[receiver.Name] = receiver
	}
	rcvs := make([]*definition.PostableApiReceiver, 0, len(nameToReceiver))
	for _, rcv := range nameToReceiver {
		rcvs = append(rcvs, rcv)
	}
	amCfg.AlertmanagerConfig.Receivers = rcvs

	rawCfg, err := json.Marshal(amCfg.AlertmanagerConfig)
	if err != nil {
		return amConfig{}, fmt.Errorf("failed to marshal Grafana Alertmanager configuration %w", err)
	}

	return amConfig{
		AlertConfigDesc:    alertspb.ToProto(string(rawCfg), amCfg.Templates, gCfg.User),
		tmplExternalURL:    externalURL,
		staticHeaders:      gCfg.StaticHeaders,
		usingGrafanaConfig: true,
	}, nil
}
