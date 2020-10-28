import WebSocket from 'ws';
import { config } from '../lib/config';
import * as utils from '../lib/utils';
import { IEventAssetDefinitionCreated, IEventStreamMessage } from '../lib/interfaces';
import * as membersHandler from '../handlers/members';
import * as assetDefinitionsHandler from '../handlers/asset-definitions';
import { IEventMemberRegistered } from '../lib/interfaces';

let ws: WebSocket;
let disconnectionDetected = false;

export const init = () => {
  ws = new WebSocket(config.eventStreams.wsEndpoint, {
    headers: {
      Authorization: 'Basic ' + Buffer.from(`${config.appCredentials.user}:${config.appCredentials.password}`).toString('base64')
    }
  });
  addEventHandlers();
};

const addEventHandlers = () => {
  ws.on('open', () => {
    if (disconnectionDetected) {
      disconnectionDetected = false;
      console.log('Event stream websocket restored');
    }
    ws.send(JSON.stringify({
      type: 'listen',
      topic: config.eventStreams.topic
    }));
  }).on('close', () => {
    disconnectionDetected = true;
    console.log(`Event stream websocket disconnected, attempting to reconnect in ${utils.constants.EVENT_STREAM_WEBSOCKET_RECONNECTION_DELAY_SECONDS} second(s)`);
    setTimeout(() => {
      init();
    }, utils.constants.EVENT_STREAM_WEBSOCKET_RECONNECTION_DELAY_SECONDS * 1000);
  }).on('message', async (message: string) => {
    await handleMessage(message);
    ws.send(JSON.stringify({
      type: 'ack',
      topic: config.eventStreams.topic
    }));
  }).on('error', err => {
    console.log(`Event stream websocket error. ${err}`);
  });
};

const handleMessage = async (message: string) => {
  const messageArray: Array<IEventStreamMessage> = JSON.parse(message);
  for (const message of messageArray) {
    switch (message.signature) {
      case utils.contractEventSignatures.MEMBER_REGISTERED:
        await membersHandler.handleMemberRegisteredEvent(message.data as IEventMemberRegistered); break;
      case utils.contractEventSignatures.DESCRIBED_STRUCTURED_ASSET_DEFINITION_CREATED:
      case utils.contractEventSignatures.DESCRIBED_UNSTRUCTURED_ASSET_DEFINITION_CREATED:
      case utils.contractEventSignatures.STRUCTURED_ASSET_DEFINITION_CREATED:
      case utils.contractEventSignatures.UNSTRUCTURED_ASSET_DEFINITION_CREATED:
        await assetDefinitionsHandler.handleAssetDefinitionCreatedEvent(message.data as IEventAssetDefinitionCreated); break;

    }
  }
};