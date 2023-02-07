//
// Copyright (C) 2022 IOTech Ltd
// Copyright (C) 2023 Intel Inc.
//
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	bootstrapContainer "github.com/edgexfoundry/go-mod-bootstrap/v3/bootstrap/container"
	"github.com/edgexfoundry/go-mod-bootstrap/v3/di"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/clients/logger"
	"github.com/edgexfoundry/go-mod-core-contracts/v3/common"
	"github.com/edgexfoundry/go-mod-messaging/v3/pkg/types"

	"github.com/edgexfoundry/edgex-go/internal/core/command/container"
)

func OnConnectHandler(requestTimeout time.Duration, dic *di.Container) mqtt.OnConnectHandler {
	return func(client mqtt.Client) {
		lc := bootstrapContainer.LoggingClientFrom(dic.Get)
		config := container.ConfigurationFrom(dic.Get)
		externalTopics := config.ExternalMQTT.Topics
		qos := config.ExternalMQTT.QoS

		requestQueryTopic := externalTopics[common.CommandQueryRequestTopicKey]
		if token := client.Subscribe(requestQueryTopic, qos, commandQueryHandler(dic)); token.Wait() && token.Error() != nil {
			lc.Errorf("could not subscribe to topic '%s': %s", requestQueryTopic, token.Error().Error())
		} else {
			lc.Debugf("Subscribed to topic '%s' on external MQTT broker", requestQueryTopic)
		}

		requestCommandTopic := externalTopics[common.CommandRequestTopicKey]
		if token := client.Subscribe(requestCommandTopic, qos, commandRequestHandler(requestTimeout, dic)); token.Wait() && token.Error() != nil {
			lc.Errorf("could not subscribe to topic '%s': %s", requestCommandTopic, token.Error().Error())
		} else {
			lc.Debugf("Subscribed to topic '%s' on external MQTT broker", requestCommandTopic)
		}
	}
}

func commandQueryHandler(dic *di.Container) mqtt.MessageHandler {
	return func(client mqtt.Client, message mqtt.Message) {
		lc := bootstrapContainer.LoggingClientFrom(dic.Get)
		lc.Debugf("Received command query request from external message broker on topic '%s' with %d bytes", message.Topic(), len(message.Payload()))

		requestEnvelope, err := types.NewMessageEnvelopeFromJSON(message.Payload())
		if err != nil {
			lc.Errorf("Failed to decode request MessageEnvelope: %s", err.Error())
			lc.Warn("Not publishing error message back due to insufficient information on response topic")
			return
		}

		externalMQTTInfo := container.ConfigurationFrom(dic.Get).ExternalMQTT
		responseTopic := externalMQTTInfo.Topics[common.ExternalCommandQueryResponseTopicKey]
		if responseTopic == "" {
			lc.Error("QueryResponseTopic not provided in External.Topics")
			lc.Warn("Not publishing error message back due to insufficient information on response topic")
			return
		}

		// example topic scheme: edgex/commandquery/request/<device-name>
		// deviceName is expected to be at last topic level.
		topicLevels := strings.Split(message.Topic(), "/")
		deviceName := topicLevels[len(topicLevels)-1]
		if strings.EqualFold(deviceName, common.All) {
			deviceName = common.All
		}

		responseEnvelope, err := getCommandQueryResponseEnvelope(requestEnvelope, deviceName, dic)
		if err != nil {
			responseEnvelope = types.NewMessageEnvelopeWithError(requestEnvelope.RequestID, err.Error())
		}

		qos := externalMQTTInfo.QoS
		retain := externalMQTTInfo.Retain
		publishMessage(client, responseTopic, qos, retain, responseEnvelope, lc)
	}
}

func commandRequestHandler(requestTimeout time.Duration, dic *di.Container) mqtt.MessageHandler {
	return func(client mqtt.Client, message mqtt.Message) {
		lc := bootstrapContainer.LoggingClientFrom(dic.Get)
		lc.Debugf("Received command request from external message broker on topic '%s' with %d bytes", message.Topic(), len(message.Payload()))

		externalMQTTInfo := container.ConfigurationFrom(dic.Get).ExternalMQTT
		qos := externalMQTTInfo.QoS
		retain := externalMQTTInfo.Retain

		requestEnvelope, err := types.NewMessageEnvelopeFromJSON(message.Payload())
		if err != nil {
			lc.Errorf("Failed to decode request MessageEnvelope: %s", err.Error())
			lc.Warn("Not publishing error message back due to insufficient information on response topic")
			return
		}

		topicLevels := strings.Split(message.Topic(), "/")
		length := len(topicLevels)
		if length < 3 {
			lc.Error("Failed to parse and construct response topic scheme, expected request topic scheme: '#/<device-name>/<command-name>/<method>")
			lc.Warn("Not publishing error message back due to insufficient information on response topic")
			return
		}

		// expected external command request/response topic scheme: #/<device-name>/<command-name>/<method>
		deviceName := topicLevels[length-3]
		commandName := topicLevels[length-2]
		method := topicLevels[length-1]
		if !strings.EqualFold(method, "get") && !strings.EqualFold(method, "set") {
			lc.Errorf("Unknown request method: %s, only 'get' or 'set' is allowed", method)
			lc.Warn("Not publishing error message back due to insufficient information on response topic")
			return
		}

		externalResponseTopic := strings.Join([]string{externalMQTTInfo.Topics[common.ExternalCommandResponseTopicPrefixKey], deviceName, commandName, method}, "/")

		internalMessageBusInfo := container.ConfigurationFrom(dic.Get).MessageBus
		deviceServiceName, deviceRequestTopic, err := validateRequestTopic(internalMessageBusInfo.Topics[common.DeviceCommandRequestTopicPrefixKey], deviceName, commandName, method, dic)
		if err != nil {
			responseEnvelope := types.NewMessageEnvelopeWithError(requestEnvelope.RequestID, err.Error())
			publishMessage(client, externalResponseTopic, qos, retain, responseEnvelope, lc)
			return
		}

		err = validateGetCommandQueryParameters(requestEnvelope.QueryParams)
		if err != nil {
			responseEnvelope := types.NewMessageEnvelopeWithError(requestEnvelope.RequestID, err.Error())
			publishMessage(client, externalResponseTopic, qos, retain, responseEnvelope, lc)
			return
		}

		internalMessageBus := bootstrapContainer.MessagingClientFrom(dic.Get)

		lc.Debugf("Sending Command request to internal MessageBus. Topic: %s, Request-id: %s Correlation-id: %s", deviceRequestTopic, requestEnvelope.RequestID, requestEnvelope.CorrelationID)

		// Request waits for the response and returns it.
		response, err := internalMessageBus.Request(requestEnvelope, deviceServiceName, deviceRequestTopic, requestTimeout)
		if err != nil {
			errorMessage := fmt.Sprintf("Failed to send DeviceCommand request with internal MessageBus: %v", err)
			responseEnvelope := types.NewMessageEnvelopeWithError(requestEnvelope.RequestID, errorMessage)
			publishMessage(client, externalResponseTopic, qos, retain, responseEnvelope, lc)
			return
		}

		lc.Debugf("Command response received from internal MessageBus. Topic: %s, Request-id: %s Correlation-id: %s", response.RequestID, response.CorrelationID)

		publishMessage(client, externalResponseTopic, qos, retain, *response, lc)
	}
}

func publishMessage(client mqtt.Client, responseTopic string, qos byte, retain bool, message types.MessageEnvelope, lc logger.LoggingClient) {
	if message.ErrorCode == 1 {
		lc.Error(string(message.Payload))
	}

	envelopeBytes, _ := json.Marshal(&message)

	if token := client.Publish(responseTopic, qos, retain, envelopeBytes); token.Wait() && token.Error() != nil {
		lc.Errorf("Could not publish to external message broker on topic '%s': %s", responseTopic, token.Error())
	} else {
		lc.Debugf("Published response message to external message broker on topic '%s' with %d bytes", responseTopic, len(envelopeBytes))
	}
}
