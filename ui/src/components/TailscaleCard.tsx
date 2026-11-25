import { isOnDevice } from "@/main";
import { LuRefreshCcw } from "react-icons/lu";
import { useCallback, useEffect, useState } from "react";

import { Button } from "@components/Button";
import { GridCard } from "@components/Card";
import { InputFieldWithLabel } from "@components/InputField";
import { NestedSettingsGroup } from "@components/NestedSettingsGroup";
import { SelectMenuBasic } from "@components/SelectMenuBasic";
import { SettingsItem } from "@components/SettingsItem";
import { TailscaleStatus } from "@hooks/stores";
import { useJsonRpc } from "@hooks/useJsonRpc";
import { m } from "@localizations/messages.js";
import notifications from "@/notifications";

const defaultControlURL = "https://controlplane.tailscale.com";
const controlServerModeDefault = "default";
const controlServerModeCustom = "custom";
type ControlServerMode = typeof controlServerModeDefault | typeof controlServerModeCustom;

export default function TailscaleCard() {
  const { send } = useJsonRpc();

  const [status, setStatus] = useState<TailscaleStatus | null>(null);
  const [controlURLInput, setControlURLInput] = useState("");
  const [controlServerMode, setControlServerMode] =
    useState<ControlServerMode>(controlServerModeDefault);
  const [isSavingControlURL, setIsSavingControlURL] = useState(false);

  const [changingAddon, setChangingAddon] = useState(false);
  const refreshStatus = useCallback((refreshControl = true) => {
    send("getTailscaleStatus", {}, resp => {
      if ("error" in resp) {
        setStatus(null);
        return;
      }
      const nextStatus = resp.result as TailscaleStatus;
      setStatus(nextStatus);
      if (!refreshControl) return;
      const activeControlURL = nextStatus.controlURL ?? defaultControlURL;
      if (activeControlURL === defaultControlURL) {
        setControlServerMode(controlServerModeDefault);
        setControlURLInput("");
      } else {
        setControlServerMode(controlServerModeCustom);
        setControlURLInput(activeControlURL);
      }
    });
  }, [send]);

  const saveControlURL = useCallback(() => {
    setIsSavingControlURL(true);
    const nextControlURL =
      controlServerMode === controlServerModeDefault ? "" : controlURLInput.trim();

    send("setTailscaleControlURL", { controlURL: nextControlURL }, resp => {
      setIsSavingControlURL(false);
      if ("error" in resp) {
        const errorMessage =
          typeof resp.error.data === "string" ? resp.error.data : resp.error.message;
        notifications.error(m.tailscale_control_server_update_failed({ error: errorMessage }));
        return;
      }

      notifications.success(m.tailscale_control_server_update_success());
      refreshStatus();
    });
  }, [controlServerMode, controlURLInput, refreshStatus, send]);

  useEffect(() => {
    refreshStatus();
  }, [refreshStatus]);

  const pendingLogin = Boolean(status?.addon?.enabled && !status.running);
  useEffect(() => {
    if (!pendingLogin) return;
    const timer = window.setInterval(() => refreshStatus(false), 2500);
    return () => window.clearInterval(timer);
  }, [pendingLogin, refreshStatus]);

  async function upload(file: File) {
    if (!status?.addon || changingAddon) return;
    if (file.size !== status.addon.archiveBytes) {
      notifications.error(m.q1_tailscale_wrong_archive({ version: status.addon.version }));
      return;
    }
    setChangingAddon(true);
    try {
      const response = await fetch("/device/tailscale-addon", {
        method: "POST", headers: { "Content-Type": "application/octet-stream" }, body: file,
      });
      if (!response.ok) {
        const body = await response.json().catch(() => null);
        throw new Error(body?.error || response.statusText);
      }
      refreshStatus(false);
    } catch (error) {
      notifications.error(String(error));
    } finally {
      setChangingAddon(false);
    }
  }

  function toggle(enabled: boolean) {
    if (changingAddon) return;
    setChangingAddon(true);
    send("setQ1TailscaleEnabled", { enabled }, response => {
      setChangingAddon(false);
      if ("error" in response) {
        const { data, message } = response.error;
        notifications.error(typeof data === "string" ? data : message);
      }
      refreshStatus(false);
    });
  }

  if (status === null || (!status.installed && !status.addon)) {
    return null;
  }

  const ipv4 = status.self?.tailscaleIPs?.find(ip => !ip.includes(":"));
  const ipv6 = status.self?.tailscaleIPs?.find(ip => ip.includes(":"));

  return (
    <GridCard>
      <div className="animate-fadeIn p-4 text-black opacity-0 animation-duration-500 dark:text-white">
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-x-2">
              <h3 className="text-base font-bold text-slate-900 dark:text-white">
                {m.tailscale_title()}
              </h3>
              {status.installed && <StatusBadge status={status} />}
            </div>

            <div>
              <Button
                size="XS"
                theme="light"
                type="button"
                text={m.tailscale_refresh()}
                LeadingIcon={LuRefreshCcw}
                onClick={() => refreshStatus()}
              />
            </div>
          </div>

          {status.addon && !status.installed && (
            <div className="space-y-2 text-sm text-slate-600 dark:text-slate-400">
              <p>{m.q1_tailscale_space({
                required: (status.addon.requiredBytes / 1048576).toFixed(1),
                free: (status.addon.freeBytes / 1048576).toFixed(1),
              })}</p>
              <a className="inline-block text-blue-600 underline dark:text-blue-400"
                href={`https://pkgs.tailscale.com/stable/tailscale_${status.addon.version}_arm.tgz`}
                target="_blank" rel="noopener noreferrer">
                {m.q1_tailscale_download({ version: status.addon.version })}
              </a>
              {isOnDevice ? (
                <label className="block">
                  {m.q1_tailscale_install()}
                  <input type="file" accept=".tgz" disabled={changingAddon || status.addon.freeBytes < status.addon.requiredBytes}
                    className="mt-1 block w-full"
                    onChange={e => { const file = e.target.files?.[0]; e.target.value = ""; if (file) void upload(file); }} />
                </label>
              ) : <p>{m.q1_tailscale_local_install()}</p>}
              {changingAddon && <p role="status">{m.q1_tailscale_installing()}</p>}
            </div>
          )}
          {status.addon && status.installed && (
            <Button size="SM" type="button" disabled={changingAddon}
              text={status.addon.enabled ? m.q1_tailscale_disable() : m.q1_tailscale_enable()}
              onClick={() => toggle(!status.addon?.enabled)} />
          )}

          <div className="space-y-4 border-t border-slate-800/10 pt-3 dark:border-slate-300/20">
            <SettingsItem
              size="SM"
              title={m.tailscale_control_server_title()}
              description={m.tailscale_control_server_description()}
            >
              <SelectMenuBasic
                size="XS"
                label=""
                value={controlServerMode}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) =>
                  setControlServerMode(e.target.value as ControlServerMode)
                }
                options={[
                  { value: controlServerModeDefault, label: m.tailscale_control_server_default() },
                  { value: controlServerModeCustom, label: m.tailscale_control_server_custom() },
                ]}
              />
            </SettingsItem>

            {controlServerMode === controlServerModeCustom && (
              <NestedSettingsGroup>
                <InputFieldWithLabel
                  size="SM"
                  label={m.tailscale_control_server_custom_url_label()}
                  placeholder={m.tailscale_control_server_custom_url_placeholder()}
                  value={controlURLInput}
                  onChange={e => setControlURLInput(e.target.value)}
                />
                <div className="flex items-center gap-x-2">
                  <Button
                    size="SM"
                    theme="primary"
                    type="button"
                    text={isSavingControlURL ? m.tailscale_saving() : m.tailscale_save()}
                    disabled={isSavingControlURL}
                    onClick={saveControlURL}
                  />
                </div>
              </NestedSettingsGroup>
            )}
          </div>

          {status.running && status.self && (
            <div className="flex-1 space-y-2">
              {status.self.hostName && (
                <div className="flex justify-between border-slate-800/10 pt-2 dark:border-slate-300/20">
                  <span className="text-sm text-slate-600 dark:text-slate-400">
                    {m.tailscale_hostname()}
                  </span>
                  <span className="text-sm font-medium">{status.self.hostName}</span>
                </div>
              )}

              {ipv4 && (
                <div className="flex justify-between border-t border-slate-800/10 pt-2 dark:border-slate-300/20">
                  <span className="text-sm text-slate-600 dark:text-slate-400">
                    {m.tailscale_ipv4()}
                  </span>
                  <span className="font-mono text-[13px] font-medium">{ipv4}</span>
                </div>
              )}

              {ipv6 && (
                <div className="flex justify-between border-t border-slate-800/10 pt-2 dark:border-slate-300/20">
                  <span className="text-sm text-slate-600 dark:text-slate-400">
                    {m.tailscale_ipv6()}
                  </span>
                  <span className="font-mono text-[13px] font-medium">{ipv6}</span>
                </div>
              )}

              {status.self.dnsName && (
                <div className="flex justify-between border-t border-slate-800/10 pt-2 dark:border-slate-300/20">
                  <span className="text-sm text-slate-600 dark:text-slate-400">
                    {m.tailscale_dns_name()}
                  </span>
                  <span className="text-sm font-medium">
                    {status.self.dnsName.replace(/\.$/, "")}
                  </span>
                </div>
              )}
            </div>
          )}

          {status.addon?.enabled && status.backendState === "NeedsLogin" && !status.authURL && (
            <Button size="SM" type="button" disabled={changingAddon} text={m.q1_tailscale_login()} onClick={() => {
              setChangingAddon(true);
              send("beginQ1TailscaleLogin", {}, response => {
                setChangingAddon(false);
                if ("error" in response) notifications.error(response.error.message);
                refreshStatus(false);
              });
            }} />
          )}
          {status.backendState === "NeedsLogin" && status.authURL && /^https?:\/\//i.test(status.authURL) && (
            <div className="space-y-2 pt-2">
              <p className="text-sm text-slate-600 dark:text-slate-400">
                {m.tailscale_auth_description()}
              </p>
              <a
                href={status.authURL}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-block text-sm font-medium text-blue-500 hover:text-blue-600 dark:text-blue-400 dark:hover:text-blue-300"
              >
                {status.authURL}
              </a>
            </div>
          )}

          {status.installed && !status.running && status.backendState !== "NeedsLogin" && (
            <p className="pt-2 text-sm text-slate-600 dark:text-slate-400">
              {m.tailscale_installed_not_running()}
              {status.backendState &&
                m.tailscale_installed_not_running_state({ state: status.backendState })}
            </p>
          )}
        </div>
      </div>
    </GridCard>
  );
}

function StatusBadge({ status }: { status: TailscaleStatus }) {
  if (status.running) {
    return (
      <span className="rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-700 dark:bg-green-900/30 dark:text-green-400">
        {m.tailscale_connected()}
      </span>
    );
  }
  if (status.backendState === "NeedsLogin") {
    return (
      <span className="rounded-full bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-400">
        {m.tailscale_needs_login()}
      </span>
    );
  }
  return (
    <span className="rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-600 dark:bg-slate-700 dark:text-slate-400">
      {m.tailscale_stopped()}
    </span>
  );
}
