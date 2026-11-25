import { useEffect, useState } from "react";
import { useJsonRpc } from "@/hooks/useJsonRpc";
import { m } from "@localizations/messages.js";
import { Button } from "@components/Button";
import { CheckboxWithLabel } from "@components/Checkbox";
import { InputFieldWithLabel } from "@components/InputField";
import { GridCard } from "@components/Card";

interface WireGuardSettings {
  enabled: boolean;
  address: string;
  peerKey: string;
  endpoint: string;
  allowedIPs: string[];
}

interface WireGuardStatus extends WireGuardSettings {
  publicKey: string;
  running: boolean;
  lastHandshake: number;
  error?: string;
}

export default function Q1WireGuardCard() {
  const { send } = useJsonRpc();
  const [status, setStatus] = useState<WireGuardStatus | null>(null);
  const [draft, setDraft] = useState<WireGuardSettings | null>(null);
  const [networks, setNetworks] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    let active = true;
    function refresh() {
      void send("getQ1WireGuard", {}, (reply) => {
        if (!active) return;
        if ("error" in reply) {
          setError(reply.error.data || reply.error.message);
          return;
        }
        const state = reply.result as WireGuardStatus;
        setStatus(state);
      });
    }
    refresh();
    const timer = window.setInterval(refresh, 15000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [send]);

  useEffect(() => {
    if (status && !draft) {
      setDraft({
        enabled: status.enabled,
        address: status.address,
        peerKey: status.peerKey,
        endpoint: status.endpoint,
        allowedIPs: status.allowedIPs,
      });
      setNetworks(status.allowedIPs.join(", "));
    }
  }, [status, draft]);

  function apply() {
    if (!draft) return;
    setSaving(true);
    setError("");
    const settings = {
      ...draft,
      allowedIPs: networks
        .split(",")
        .map((n) => n.trim())
        .filter(Boolean),
    };
    void send("setQ1WireGuard", { settings }, (reply) => {
      setSaving(false);
      if ("error" in reply) {
        setError(reply.error.data || reply.error.message);
        return;
      }
      setDraft(settings);
      void send("getQ1WireGuard", {}, (updated) => {
        if ("error" in updated) {
          setError(updated.error.data || updated.error.message);
          return;
        }
        setStatus(updated.result as WireGuardStatus);
        setDraft(settings);
      });
    });
  }

  return (
    <GridCard>
      <div className="space-y-3 p-4">
        <h3 className="font-semibold text-slate-900 dark:text-white">WireGuard</h3>
        {status && draft && (
          <>
            <InputFieldWithLabel
              type="file"
              accept=".conf,text/plain"
              label={m.q1_wg_import()}
              disabled={saving}
              onChange={async (e) => {
                const file = e.currentTarget.files?.[0];
                e.currentTarget.value = "";
                if (!file) return;
                if (file.size > 16384) {
                  setError(m.q1_wg_import_error());
                  return;
                }
                setSaving(true);
                setError("");
                try {
                  const profile = await file.text();
                  await send("importQ1WireGuard", { profile }, (response) => {
                    setSaving(false);
                    if ("error" in response) {
                      setError(response.error.data || response.error.message);
                      return;
                    }
                    void send("getQ1WireGuard", {}, (reply) => {
                      if ("error" in reply) {
                        setError(reply.error.data || reply.error.message);
                        return;
                      }
                      const state = reply.result as WireGuardStatus;
                      setStatus(state);
                      setDraft({
                        enabled: state.enabled,
                        address: state.address,
                        peerKey: state.peerKey,
                        endpoint: state.endpoint,
                        allowedIPs: state.allowedIPs,
                      });
                      setNetworks(state.allowedIPs.join(", "));
                    });
                  });
                } catch {
                  setSaving(false);
                  setError(m.q1_wg_import_error());
                }
              }}
            />
            <CheckboxWithLabel
              id="q1-wg-enabled"
              label={m.q1_wg_enable()}
              checked={draft.enabled}
              onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })}
            />
            <InputFieldWithLabel
              label={m.q1_wg_public_key()}
              value={status.publicKey}
              readOnly
              description={m.q1_wg_peer_help()}
              onFocus={(e) => e.target.select()}
            />
            <InputFieldWithLabel
              label={m.q1_wg_address()}
              placeholder="192.0.2.5"
              value={draft.address}
              onChange={(e) => setDraft({ ...draft, address: e.target.value })}
              autoComplete="off"
            />
            <InputFieldWithLabel
              label={m.q1_wg_peer_key()}
              value={draft.peerKey}
              onChange={(e) => setDraft({ ...draft, peerKey: e.target.value })}
              autoComplete="off"
            />
            <InputFieldWithLabel
              label={m.q1_wg_endpoint()}
              placeholder="vpn.example.com:51820"
              value={draft.endpoint}
              onChange={(e) => setDraft({ ...draft, endpoint: e.target.value })}
              autoComplete="off"
            />
            <InputFieldWithLabel
              label={m.q1_wg_networks()}
              placeholder="192.0.2.0/24"
              value={networks}
              onChange={(e) => setNetworks(e.target.value)}
              autoComplete="off"
            />
            {status.running && (
              <p className="text-sm" role="status">
                {status.lastHandshake
                  ? m.q1_wg_handshake({
                      time: new Date(status.lastHandshake * 1000).toLocaleString(),
                    })
                  : m.q1_wg_waiting()}
              </p>
            )}
            <Button
              type="button"
              size="SM"
              disabled={saving}
              text={m.network_save_settings()}
              onClick={apply}
            />
          </>
        )}
        {(error || status?.error) && (
          <p role="alert" className="text-sm text-red-600 dark:text-red-400">
            {error || status?.error}
          </p>
        )}
      </div>
    </GridCard>
  );
}
