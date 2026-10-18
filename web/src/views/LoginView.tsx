import React, { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "@/features/auth/AuthContext";
import { Zap } from "lucide-react";

export const LoginView: React.FC = () => {
  const { login, loading, error } = useAuth();
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const ok = await login(username, password);
    if (ok) {
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
          <h2>登录 PulseMedia</h2>
          <p className="subtitle">现代化自托管影视自动化控制中枢</p>
        </div>

        {error && <div className="alert-error">{error}</div>}

        <form onSubmit={handleSubmit} className="auth-form">
          <div className="form-group">
            <label>用户名</label>
            <div className="input-wrap">
              <input
                type="text"
                required
                autoFocus
                placeholder="输入管理员用户名..."
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            </div>
          </div>

          <div className="form-group">
            <label>账户密码</label>
            <div className="input-wrap">
              <input
                type="password"
                required
                placeholder="输入密码..."
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
          </div>

          <button type="submit" className="btn-auth" disabled={loading}>
            {loading ? "正在验证登录..." : "进入系统控制台 →"}
          </button>
        </form>

        <div className="auth-footer">
          <div className="feature-pills">
            <span>• 影视订阅意图管理</span>
            <span>• 站点聚合搜索</span>
            <span>• 下载任务状态对账</span>
          </div>
        </div>
      </div>
    </div>
  );
};
