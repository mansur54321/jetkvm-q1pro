import { useEffect, useState } from "react";
import { useJsonRpc } from "@hooks/useJsonRpc";
import { useRTCStore } from "@hooks/stores";
import { Button } from "@components/Button";
import Card from "@components/Card";
import { SettingsSectionHeader } from "@components/SettingsSectionHeader";
import { m } from "@localizations/messages";

interface BluetoothWifiStatus {
  active: boolean;
  handoffUntil: number;
  radioFailed: boolean;
  pairCode?: string;
}

export default function BluetoothWifiSetup() {
  const { send } = useJsonRpc();
  const channel = useRTCStore(state => state.rpcDataChannel);
  const [status, setStatus] = useState<BluetoothWifiStatus | null>(null);
  const [error, setError] = useState(false);
  useEffect(() => {
    if (!channel || channel.readyState !== "open") return;
    let live = true;
    const poll = () =>
      send("getBluetoothWifiStatus", {}, response => {
        if (live && "result" in response) setStatus(response.result as BluetoothWifiStatus);
      });
    poll();
    const timer = setInterval(poll, 5000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [channel, send]);
  const armed = !!status?.handoffUntil;
  function handoff() {
    send("setBluetoothWifiHandoff", { enable: !armed && !status?.active }, response => {
      if ("error" in response) setError(true);
      else {
        setStatus(response.result as BluetoothWifiStatus);
        setError(false);
      }
    });
  }
  return (
    <section className="space-y-2">
      <SettingsSectionHeader title={m.wifi_ble_title()} description={m.wifi_ble_description()} />
      <Card className="space-y-3 p-4 text-sm">
        <p>{m.wifi_ble_gestures()}</p>
        <p>{m.wifi_ble_confirm()}</p>
        <p>{m.wifi_ble_pairing()}</p>
        {status?.pairCode && <p role="status">{m.wifi_ble_pairing_code({ code: status.pairCode })}</p>}
        <div className="flex flex-wrap items-center gap-3">
          <Button
            type="button"
            size="SM"
            theme="light"
            disabled={!status}
            onClick={handoff}
            text={armed || status?.active ? m.wifi_ble_cancel() : m.wifi_ble_arm()}
          />
          {armed && (
            <span>
              {m.wifi_ble_armed({ time: new Date(status!.handoffUntil * 1000).toLocaleString() })}
            </span>
          )}
          {status?.active && <span>{m.wifi_ble_active()}</span>}
        </div>
        {error && <p role="alert">{m.wifi_ble_handoff_error()}</p>}
        {status?.radioFailed && <p role="status">{m.wifi_ble_radio_error()}</p>}
        <p>{m.wifi_ble_browser_hint()}</p>
        <div className="flex flex-wrap gap-4">
          <a
            className="underline"
            href="https://toolbox.openhomefoundation.org/improv/"
            target="_blank"
            rel="noreferrer"
          >
            {m.wifi_ble_open_client()}
          </a>
          <a className="underline" href="/static/q1pro-wifi.html" download="q1pro-wifi.html">
            {m.wifi_ble_download_client()}
          </a>
        </div>
      </Card>
    </section>
  );
}
