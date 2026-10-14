import { Kbd, Modal } from "./ui";

const groups: [string, [string[], string][]][] = [
  [
    "Navigate",
    [
      [["1"], "Instances"],
      [["2"], "Routes"],
      [["3"], "Daemon"],
      [["↑", "↓"], "Select a row (also j / k)"],
      [["Home", "End"], "First / last row"],
      [["Enter"], "Open instance or service, expand a value"],
      [["Esc"], "Back, or clear the search"],
      [["/"], "Search this list"],
      [["d"], "Full selection details"],
    ],
  ],
  [
    "Instance",
    [
      [["s"], "Services"],
      [["u"], "URLs"],
      [["e"], "Environment"],
      [["l"], "Logs"],
      [["[", "]"], "Previous / next service"],
      [["r"], "Restart instance"],
      [["x"], "Stop instance (graceful)"],
    ],
  ],
  [
    "Values",
    [
      [["o"], "Open selected URL in a new tab"],
      [["c"], "Copy URL or environment value"],
      [["Y"], "Copy NAME=value"],
      [["v"], "Reveal / hide sensitive environment"],
      [["i"], "Include / exclude inherited environment"],
    ],
  ],
  [
    "Dashboard",
    [
      [["p"], "Pause / resume automatic refresh"],
      [["?"], "This help"],
    ],
  ],
];

export function HelpModal({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} wide>
      <div className="help-grid">
        {groups.map(([title, items]) => (
          <section key={title}>
            <h3>{title}</h3>
            <dl>
              {items.map(([keys, label]) => (
                <div className="help-row" key={label}>
                  <dt>
                    {keys.map((key) => (
                      <Kbd key={key}>{key}</Kbd>
                    ))}
                  </dt>
                  <dd>{label}</dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
      <p className="muted small">
        Stop and restart apply to the entire instance, including all of its services. Copy works while sensitive values
        stay hidden. Closing this page leaves instances running.
      </p>
    </Modal>
  );
}
