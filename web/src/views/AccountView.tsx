import {useAuth} from "@/features/auth/AuthContext";
import {AccountTokensView} from "./AccountTokensView";
import {AccountManagementPanel} from "@/features/auth/AccountManagementPanel";
import {AccountSecurityPanel} from "@/features/auth/AccountSecurityPanel";

export function AccountView(){
 const {user}=useAuth();
 if(!user)return null;
 return <div key={`${user.id}:${user.role}`} className="settings-container"><div className="page-header"><div><h1>账号与安全</h1><p className="subtitle">管理家庭成员、密码、登录会话、双因素验证与个人 API 令牌。</p></div></div><AccountSecurityPanel/><AccountManagementPanel isAdmin={user.role==="admin"}/><AccountTokensView/></div>;
}
