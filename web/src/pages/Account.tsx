import { CheckinCard } from '@/components/CheckinCard'
import { InviteCard } from '@/components/InviteCard'
import { MembershipCard } from '@/components/MembershipCard'
import { PageHeader } from '@/components/PageHeader'
import { Crown } from 'lucide-react'

/**
 * Account 是普通用户的「我的账户」页。
 *
 * 会员状态、每日签到、邀请好友原本挤在总览页上，把真正要看的统计数据
 * 推到了首屏之外 —— 手机上要滑三屏才看到查询量。这三块都是低频操作，
 * 收敛到独立页面，总览只留数据。
 */
export default function Account() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="我的账户"
        description="会员状态、每日签到与邀请好友"
        icon={<Crown className="size-5" />}
      />
      <MembershipCard />
      <div className="grid gap-4 lg:grid-cols-2">
        <CheckinCard />
        <InviteCard />
      </div>
    </div>
  )
}
