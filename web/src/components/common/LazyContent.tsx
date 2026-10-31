import React, { Suspense } from "react";

// Key this boundary by account and scope so a pending module cannot preserve an old screen.
export class LazyContent extends React.Component<{ children: React.ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed) {
      return <section role="alert">
        <p>页面加载失败，请重新加载。</p>
        <button type="button" className="btn-secondary" onClick={() => window.location.reload()}>重新加载页面</button>
      </section>;
    }
    return <Suspense fallback={<p role="status" aria-live="polite">正在加载…</p>}>
      {this.props.children}
    </Suspense>;
  }
}
