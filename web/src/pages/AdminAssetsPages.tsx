import { ShieldCheck, Users } from 'lucide-react'

import { AdminUserRulesPanel, AdminUserSubsPanel } from '@/components/AdminUserAssetsPanel'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import Rules from '@/pages/Rules'
import Subscriptions from '@/pages/Subscriptions'
import { AdminPage } from '@/pages/Admin'

/**
 * 管理员的规则页：全局层 + 用户层两个 Tab。
 *
 * 之前只有全局规则，普通用户自己加的规则在后台完全不可见 ——
 * 用户说「某域名被拦了」，管理员翻不到那条规则，只能让他自己截图。
 */
export function AdminRulesPage() {
  return (
    <AdminPage
      title="规则"
      description="全局规则作用于所有租户；用户规则是各用户自己加的，可查看与删除"
      icon={<ShieldCheck className="size-5" />}
    >
      <Tabs defaultValue="global">
        <TabsList>
          <TabsTrigger value="global">全局规则</TabsTrigger>
          <TabsTrigger value="user">用户规则</TabsTrigger>
        </TabsList>
        <TabsContent value="global" className="pt-4">
          <Rules scope="global" />
        </TabsContent>
        <TabsContent value="user" className="pt-4">
          <AdminUserRulesPanel />
        </TabsContent>
      </Tabs>
    </AdminPage>
  )
}

/** 管理员的订阅页：全局层 + 用户层两个 Tab。 */
export function AdminSubsPage() {
  return (
    <AdminPage
      title="订阅"
      description="全局订阅作用于所有租户；用户订阅是各用户自己加的，只读查看"
      icon={<Users className="size-5" />}
    >
      <Tabs defaultValue="global">
        <TabsList>
          <TabsTrigger value="global">全局订阅</TabsTrigger>
          <TabsTrigger value="user">用户订阅</TabsTrigger>
        </TabsList>
        <TabsContent value="global" className="pt-4">
          <Subscriptions scope="global" />
        </TabsContent>
        <TabsContent value="user" className="pt-4">
          <AdminUserSubsPanel />
        </TabsContent>
      </Tabs>
    </AdminPage>
  )
}
