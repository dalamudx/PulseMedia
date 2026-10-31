import React, { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "@/features/auth/AuthContext";
import { Zap } from "lucide-react";

export const SetupView: React.FC = () => {
  const { setup, loading, error } = useAuth();
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [deploymentToken, setDeploymentToken] = useState("");

  const passwordMismatch = Boolean(
    password && confirmPassword && password !== confirmPassword
  );

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (passwordMismatch) return;
    const ok = await setup(username, password, deploymentToken);
    if (ok) {
      setDeploymentToken("");
      navigate("/");
    }
  };

  return (
    <div className="auth-wrapper">
      <div className="ambient-glow"></div>

      <div className="auth-card">
        <div className="brand-header">
          <div className="logo-box">
            <Zap size={28} />
          </div>
          <h2>初始化管理员账户</h2>
          <p className="subtitle">欢迎使用 PulseMedia，首次运行请创建初始管理员凭据</p>
        </div>

        {error && <div className="alert-error">{error}</div>}

        <form onSubmit={handleSubmit} className="auth-form">
          <div className="form-group">
            <label>管理员用户名 (至少 3 位)</label>
            <div className="input-wrap">
              <input
                type="text"
                required
                minLength={3}
                autoFocus
                placeholder="admin"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            </div>
          </div>

          <div className="form-group">
            <label>登录密码 (至少 8 位)</label>
            <div className="input-wrap">
              <input
                type="password"
                required
                minLength={8}
                placeholder="输入安全密码..."
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
          </div>

          <div className="form-group">
            <label>确认登录密码</label>
            <div className="input-wrap">
              <input
                type="password"
                required
                minLength={8}
                placeholder="再次输入确认..."
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
              />
            </div>
          </div>

          <div className="form-group">
            <label htmlFor="setup-deployment-token">部署令牌（本地直连可留空）</label>
            <div className="input-wrap">
              <input
                id="setup-deployment-token"
                type="password"
                autoComplete="off"
                spellCheck={false}
                value={deploymentToken}
                onChange={(e) => setDeploymentToken(e.target.value)}
                aria-describedby="setup-token-help"
              />
            </div>
            <small id="setup-token-help">通过局域网或容器映射端口初始化时，请填写部署者提供的令牌。</small>
          </div>

          {passwordMismatch && (
            <div className="alert-error">两次输入的密码不一致</div>
          )}

          <button
            type="submit"
            className="btn-auth"
            disabled={loading || passwordMismatch}
          >
            {loading ? "正在初始化..." : "创建管理员并启动系统 →"}
          </button>
        </form>

        <div className="auth-footer">
          <div className="feature-pills">
            <span>• PostgreSQL 18 原生驱动</span>
            <span>• 事务级安全存储</span>
            <span>• 单二进制内嵌</span>
          </div>
        </div>
      </div>
    </div>
  );
};
