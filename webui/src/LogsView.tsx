import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button, Empty } from "./components/ui";

interface Props {
  lines: string[] | undefined;
  error?: string;
  loading: boolean;
  filter: string;
  onExpand: (line: string) => void;
}

/** Recent saved output; staying at the bottom follows new output. */
export function LogsView({ lines, error, loading, filter, onExpand }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const visible = (lines ?? []).filter((line) => !filter || line.toLowerCase().includes(filter.toLowerCase()));

  useLayoutEffect(() => {
    const element = ref.current;
    if (element && following) element.scrollTop = element.scrollHeight;
  }, [visible.length, lines, following]);

  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const onScroll = () => setFollowing(element.scrollHeight - element.scrollTop - element.clientHeight < 24);
    element.addEventListener("scroll", onScroll, { passive: true });
    return () => element.removeEventListener("scroll", onScroll);
  }, []);

  return (
    <div className="logs-wrap">
      <div className="logs" ref={ref} tabIndex={0} aria-label="Service logs">
        {visible.length === 0 ? (
          <Empty
            title={error ? error : loading && !lines ? "Loading logs…" : filter ? "No matching lines" : "No saved log output"}
            hint={!error && !loading && !filter ? "Logging may be disabled or unavailable for this wrapper." : undefined}
          />
        ) : (
          visible.map((line, index) => (
            <div className="log-line" key={index} onDoubleClick={() => onExpand(line)}>
              <span className="log-number">{index + 1}</span>
              <span className="log-text">{line || " "}</span>
            </div>
          ))
        )}
      </div>
      {!following && visible.length > 0 && (
        <div className="logs-follow">
          <Button
            small
            variant="primary"
            icon="arrowDown"
            onClick={() => {
              setFollowing(true);
              const element = ref.current;
              if (element) element.scrollTop = element.scrollHeight;
            }}
          >
            Follow
          </Button>
        </div>
      )}
    </div>
  );
}
